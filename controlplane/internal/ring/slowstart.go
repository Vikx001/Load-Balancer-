package ring

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ─── WHY SLOW-START MATTERS ───────────────────────────────────────────────────
// When a backend is marked UP after a restart or recovery, the naive approach
// immediately assigns it all 150 virtual nodes (SetHealth(id, true) calls rebuild).
// This sends a full share of traffic — potentially thousands of requests/second —
// to a cache-cold server.  The result is a thundering herd:
//
//   1. Backend is cold (page cache, JIT, connection pools all empty).
//   2. Every request hits a cache miss; latency spikes 5–20×.
//   3. If the backend was recovering from OOM, the spike triggers a second OOM.
//   4. The health checker marks it DOWN again.  Repeat = restart loop.
//
// Fix: instead of restoring all vnodes at once, add them in batches of
// slowStartBatchSize every slowStartInterval.  Pause if error rate > threshold.
// Require minHealthChecksBeforeStart consecutive successes before starting at all.
//
// Traffic timeline (150 vnodes, 15-vnode batches, 30s interval):
//   t=0       backend registers 60 consecutive successes (2 min warmup)
//   t=0       Tick 1: +15 vnodes (10% traffic)
//   t=30s     Tick 2: +15 vnodes (20% traffic)
//   ...
//   t=270s    Tick 10: +15 vnodes (100% traffic, ~4.5 min total ramp)
//
// If error rate exceeds 1% during ramp: pause and wait for the next tick.

const (
	defaultSlowStartBatch     = 15 // vnodes per tick
	defaultSlowStartInterval  = 30 * time.Second
	defaultSlowStartMaxErrPct = 1.0 // percent
)

// restoreState tracks a single backend's slow-start progress.
type restoreState struct {
	backendID     uint32
	targetVnodes  int  // final vnode count (from cfg.VnodesPerServer)
	currentVnodes int  // how many have been restored so far
	paused        bool // true if error rate exceeded threshold last tick
}

// SlowStartController manages gradual vnode restoration for recovered backends.
// It is embedded in the ring.Manager for direct map access.
type SlowStartController struct {
	mu        sync.Mutex
	restoring map[uint32]*restoreState

	batchSize   int
	interval    time.Duration
	maxErrorPct float64

	log *zap.Logger

	// errorRateFn returns the current error rate (0..100) for a backend.
	// Populated by the daemon; defaults to always-0 (no pause) if nil.
	errorRateFn func(backendID uint32) float64
}

func newSlowStartController(batchSize, intervalS, maxErrPct int, log *zap.Logger) *SlowStartController {
	if batchSize <= 0 {
		batchSize = defaultSlowStartBatch
	}
	interval := defaultSlowStartInterval
	if intervalS > 0 {
		interval = time.Duration(intervalS) * time.Second
	}
	maxErr := defaultSlowStartMaxErrPct
	if maxErrPct > 0 {
		maxErr = float64(maxErrPct)
	}
	return &SlowStartController{
		restoring:   make(map[uint32]*restoreState),
		batchSize:   batchSize,
		interval:    interval,
		maxErrorPct: maxErr,
		log:         log,
	}
}

// BeginSlowStart enqueues a backend for gradual vnode restoration.
// Called by the health checker after minSuccessesBeforeRestore consecutive successes.
// targetVnodes: the full vnode count to restore to (cfg.VnodesPerServer).
func (m *Manager) BeginSlowStart(backendID uint32) {
	m.slowStart.mu.Lock()
	defer m.slowStart.mu.Unlock()

	if _, exists := m.slowStart.restoring[backendID]; exists {
		return // already restoring; ignore duplicate calls
	}

	target := m.cfg.VnodesPerServer
	if target <= 0 {
		target = defaultVnodes
	}

	// The backend currently has 0 vnodes (it was marked DOWN, which sets
	// vnodeCount to 0 via SetHealth(id,false) → rebuild()).  Start from 0.
	m.slowStart.restoring[backendID] = &restoreState{
		backendID:     backendID,
		targetVnodes:  target,
		currentVnodes: 0,
	}
	m.log.Info("slow-start initiated for recovered backend",
		zap.Uint32("backend_id", backendID),
		zap.Int("target_vnodes", target),
		zap.Int("batch_size", m.slowStart.batchSize),
		zap.Duration("interval", m.slowStart.interval),
	)
}

// SetSlowStartErrorRateFn sets the function the controller uses to check whether
// a backend's error rate is too high to continue restoring vnodes.
// The function returns the current error rate as a percentage (0–100).
func (m *Manager) SetSlowStartErrorRateFn(fn func(backendID uint32) float64) {
	m.slowStart.errorRateFn = fn
}

// RunSlowStart is the slow-start tick loop.  Run in a goroutine.
// Cancel ctx to stop.
func (m *Manager) RunSlowStart(ctx context.Context) error {
	ticker := time.NewTicker(m.slowStart.interval)
	defer ticker.Stop()

	m.log.Info("slow-start controller running",
		zap.Duration("interval", m.slowStart.interval),
		zap.Int("batch_size", m.slowStart.batchSize),
		zap.Float64("max_error_pct", m.slowStart.maxErrorPct),
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.slowStartTick()
		}
	}
}

func (m *Manager) slowStartTick() {
	m.slowStart.mu.Lock()
	var done []uint32

	for id, rs := range m.slowStart.restoring {
		// Check error rate before adding more vnodes
		errPct := 0.0
		if m.slowStart.errorRateFn != nil {
			errPct = m.slowStart.errorRateFn(id)
		}
		if errPct > m.slowStart.maxErrorPct {
			if !rs.paused {
				rs.paused = true
				m.log.Warn("slow-start paused: error rate too high",
					zap.Uint32("backend_id", id),
					zap.Float64("error_rate_pct", errPct),
					zap.Float64("threshold_pct", m.slowStart.maxErrorPct),
					zap.Int("current_vnodes", rs.currentVnodes),
				)
			}
			continue
		}
		rs.paused = false

		// Add the next batch
		newCount := rs.currentVnodes + m.slowStart.batchSize
		if newCount > rs.targetVnodes {
			newCount = rs.targetVnodes
		}
		rs.currentVnodes = newCount

		m.slowStart.mu.Unlock()
		// SetVnodeCount is thread-safe (acquires mu internally)
		m.SetVnodeCount(id, newCount)
		m.slowStart.mu.Lock()

		m.log.Info("slow-start tick",
			zap.Uint32("backend_id", id),
			zap.Int("vnodes_now", newCount),
			zap.Int("target", rs.targetVnodes),
		)

		if newCount >= rs.targetVnodes {
			m.log.Info("slow-start complete: backend at full capacity",
				zap.Uint32("backend_id", id),
				zap.Int("vnodes", newCount),
			)
			done = append(done, id)
		}
	}

	for _, id := range done {
		delete(m.slowStart.restoring, id)
	}
	m.slowStart.mu.Unlock()
}

// CancelSlowStart removes a backend from the slow-start queue.
// Call when the backend is marked DOWN during restoration.
func (m *Manager) CancelSlowStart(backendID uint32) {
	m.slowStart.mu.Lock()
	delete(m.slowStart.restoring, backendID)
	m.slowStart.mu.Unlock()
}
