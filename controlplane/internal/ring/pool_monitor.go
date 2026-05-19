package ring

import (
	"context"
	"time"

	"github.com/cilium/ebpf"
	"go.uber.org/zap"
)

// ─── WHY AN i-SOCK POOL MONITOR ──────────────────────────────────────────────
// The i-sock pool (BPF_MAP_TYPE_SOCKHASH, keyed by instance_id) holds one
// pre-warmed TCP socket per backend.  When bpf_msg_redirect_hash runs, it looks
// up this pool to find the socket to use for the current connection.
//
// Failure mode — backend restart:
//   1. Backend process exits → kernel sends TCP RST on all existing connections.
//   2. The SOCKHASH entry for that backend still maps instance_id → dead sock fd.
//   3. The next bpf_msg_redirect_hash for that backend silently fails:
//      • MSG_REDIRECT returns SK_DROP (data loss), or
//      • falls through to user-space relay with no splice (CPU spike)
//   4. The pool never self-heals; every connection to this backend fails until
//      the control plane explicitly removes and replaces the sock entry.
//
// Fix in two parts:
//   Part A (eBPF): SO_KEEPALIVE on i-socks (connection_relay.bpf.c isock_keepalive)
//     — the kernel detects the dead socket in ≤11s and generates an event.
//   Part B (Go): PoolMonitor reads the SOCKHASH size every 30s and compares it to
//     the number of healthy backends.  A hit-rate drop below 95% indicates stale
//     entries; the monitor logs a structured warning with remediation commands.
//
// Operational remediation (if the monitor fires):
//   $ bpftool map dump pinned /sys/fs/bpf/omega/isock_pool | grep -c 'key'
//   # Expected: one entry per healthy backend.  Less = stale/missing entries.
//   # Trigger reconnect by restarting the omega-lb daemon or calling the admin API:
//   $ curl -X POST http://localhost:9000/admin/reconnect-pool

const poolMonitorInterval = 30 * time.Second

// PoolMonitor checks the i-sock pool for drift relative to the healthy backend set.
type PoolMonitor struct {
	log     *zap.Logger
	ring    *Manager
	pinPath string
}

// NewPoolMonitor constructs a PoolMonitor.
// pinPath: directory where BPF maps are pinned (e.g. /sys/fs/bpf/omega).
func NewPoolMonitor(pinPath string, ring *Manager, log *zap.Logger) *PoolMonitor {
	return &PoolMonitor{
		log:     log,
		ring:    ring,
		pinPath: pinPath,
	}
}

// Run starts the pool monitor loop.  Cancel the context to stop.
func (p *PoolMonitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(poolMonitorInterval)
	defer ticker.Stop()

	p.log.Info("i-sock pool monitor started",
		zap.String("pin_path", p.pinPath),
		zap.Duration("interval", poolMonitorInterval),
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.check(); err != nil {
				p.log.Error("pool monitor check failed", zap.Error(err))
				// Non-fatal: TCP keepalive provides the primary dead-socket guard.
			}
		}
	}
}

// check reads the SOCKHASH and the healthy backend count and emits a warning
// when the hit rate drops below 95%.
func (p *PoolMonitor) check() error {
	sockMap, err := ebpf.LoadPinnedMap(p.pinPath+"/isock_pool", nil)
	if err != nil {
		// Map not yet pinned — normal during daemon startup
		return nil
	}
	defer sockMap.Close()

	// Count entries in the pool by iterating keys
	var key uint32
	var val uint64
	poolSize := 0
	iter := sockMap.Iterate()
	for iter.Next(&key, &val) {
		poolSize++
	}
	if err := iter.Err(); err != nil {
		return err
	}

	// Count healthy backends from the ring
	p.ring.mu.RLock()
	healthyCount := 0
	for _, b := range p.ring.backends {
		if b.Health {
			healthyCount++
		}
	}
	p.ring.mu.RUnlock()

	if healthyCount == 0 {
		return nil // nothing to compare against during initial startup
	}

	hitRate := float64(poolSize) / float64(healthyCount)
	p.log.Debug("i-sock pool health",
		zap.Int("pool_size", poolSize),
		zap.Int("healthy_backends", healthyCount),
		zap.Float64("hit_rate", hitRate),
	)

	if hitRate < 0.95 {
		// Some backends have no pre-warmed socket in the pool.
		// This means either:
		//   a) The backend restarted and the keepalive closed the dead socket but
		//      the control plane hasn't yet re-established the connection, or
		//   b) A new backend was added but the i-sock was never created.
		p.log.Warn("i-sock pool drift detected: some backends lack a pre-warmed socket",
			zap.Int("pool_entries", poolSize),
			zap.Int("healthy_backends", healthyCount),
			zap.Float64("hit_rate_pct", hitRate*100),
			zap.String("impact", "requests to affected backends will fail bpf_msg_redirect_hash and fall through to user-space relay (CPU spike + latency)"),
			zap.String("remediation", "bpftool map dump pinned "+p.pinPath+"/isock_pool | grep -c 'key'"),
			zap.String("remediation2", "curl -X POST http://localhost:9000/admin/reconnect-pool"),
		)
	}

	return nil
}
