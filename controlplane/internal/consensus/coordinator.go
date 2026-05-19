// Package consensus implements distributed ring-state synchronization for
// multi-node OmegaLB deployments (DaemonSet / baremetal cluster).
//
// ─── WHY DISTRIBUTED CONSENSUS IS REQUIRED ────────────────────────────────────
// Each OmegaLB node runs its own control plane and independently manages its eBPF
// ring.  Without coordination, each node sees different vnode assignments:
//
//   Node A: backend-1=90 vnodes, backend-2=60 vnodes
//   Node B: backend-1=60 vnodes, backend-2=90 vnodes
//
// This causes permanent asymmetric load even when the cluster is "balanced": each
// node routes more traffic to a different backend.  The RL agent on Node A and B
// then fight each other — A reduces vnodes for backend-2 (seeing it as overloaded),
// while B does the opposite.  The system never converges.
//
// Worse: if nodes disagree on which backends are healthy, some nodes will route to
// a DOWN backend that others have already removed.  Requests sent to those nodes
// will fail until the local health checker catches up (up to 6s gap).
//
// Fix: elect one leader (via etcd distributed lock).  The leader writes the
// canonical ring state to etcd.  All followers watch the key and apply the state.
// This provides:
//   • Single-writer consistency for ring mutations
//   • Monotonic state versioning (only newer snapshots are applied)
//   • Automatic leader failover (lock expires if leader crashes; TTL-based)
//
// Architecture:
//   Leader:   ring.Manager → Coordinator.WriteRingState → etcd /omega-lb/ring-state
//   Follower: etcd /omega-lb/ring-state → Coordinator.applySnapshot → ring.Manager
//
// Requirements:
//   go get go.etcd.io/etcd/client/v3
//   (etcd v3 clientv3, minimum etcd server 3.4)
//
// Operational commands:
//   # Check which node is leader
//   $ etcdctl get /omega-lb/leader
//   # Inspect current ring state
//   $ etcdctl get /omega-lb/ring-state | python3 -m json.tool
//   # Force leader re-election (delete the lock)
//   $ etcdctl del /omega-lb/leader
package consensus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/ring"
)

// ─── State store interface ─────────────────────────────────────────────────────
// StateStore is the persistence/coordination backend.  The production
// implementation uses etcd (EtcdStore below).  Tests can substitute a
// MemStateStore.

// StateStore is the interface that wraps etcd-like operations used by the
// Coordinator.  Implement this interface to substitute a different backend
// (Redis SETNX + WATCH, ZooKeeper, etc.).
type StateStore interface {
	// Put atomically writes key=value.  Used by the leader to publish ring state.
	Put(ctx context.Context, key string, value []byte) error

	// Get returns the current value for key and its monotonic revision/version.
	// revision is used to detect stale reads.  Returns revision=0 and value=nil
	// if the key does not exist.
	Get(ctx context.Context, key string) (value []byte, revision int64, err error)

	// Watch returns a channel that delivers value updates for key after the given
	// startRevision.  The channel is closed when ctx is cancelled.
	Watch(ctx context.Context, key string, startRevision int64) (<-chan WatchEvent, error)

	// TryLock attempts to acquire a distributed lock at key with a TTL-based
	// lease.  Returns (true, cancel) if the lock was acquired.  cancel must be
	// called to release the lock.  Returns (false, nil) if the lock is held by
	// another node.
	TryLock(ctx context.Context, key string, ttlSeconds int64) (acquired bool, cancel context.CancelFunc, err error)

	// Close releases resources.
	Close() error
}

// WatchEvent carries an updated value from the StateStore.
type WatchEvent struct {
	Value    []byte
	Revision int64
	Err      error
}

// ─── Ring state snapshot ───────────────────────────────────────────────────────

// BackendSnapshot is the serializable representation of one backend.
type BackendSnapshot struct {
	ID          uint32 `json:"id"`
	IP          [4]byte `json:"ip"`
	Port        uint16 `json:"port"`
	VnodeCount  int    `json:"vnode_count"`
	CapacityMax int64  `json:"capacity_max"`
	Health      bool   `json:"health"`
	Stateful    bool   `json:"stateful"`
}

// RingStateSnapshot is the canonical ring state written by the leader.
// Followers apply this snapshot atomically.
type RingStateSnapshot struct {
	Backends  []BackendSnapshot `json:"backends"`
	UpdatedAt time.Time         `json:"updated_at"`
	NodeID    string            `json:"node_id"` // ID of the node that wrote this
	Version   int64             `json:"version"` // monotonically increasing; followers reject older versions
}

// ─── Coordinator ──────────────────────────────────────────────────────────────

// Coordinator manages leader election and ring state synchronization.
// The zero value is not valid; use NewCoordinator.
type Coordinator struct {
	store       StateStore
	nodeID      string
	log         *zap.Logger
	ring        *ring.Manager
	isLeader    atomic.Bool
	leaderKey   string
	stateKey    string
	lockTTL     int64       // seconds
	lastVersion atomic.Int64 // last applied snapshot version; atomic for race-free GetStatus
}

// NewCoordinator creates a Coordinator.
// store: etcd (or compatible) state store.
// nodeID: unique identifier for this node (e.g. hostname, pod UID).
// leaderKey: etcd key used for leader election (e.g. /omega-lb/leader).
// stateKey: etcd key for the canonical ring state (e.g. /omega-lb/ring-state).
// lockTTLSeconds: leader lock TTL; must be > 2× the publish interval.
func NewCoordinator(
	store StateStore,
	nodeID string,
	leaderKey string,
	stateKey string,
	lockTTLSeconds int64,
	rm *ring.Manager,
	log *zap.Logger,
) *Coordinator {
	if lockTTLSeconds <= 0 {
		lockTTLSeconds = 10
	}
	return &Coordinator{
		store:     store,
		nodeID:    nodeID,
		log:       log,
		ring:      rm,
		leaderKey: leaderKey,
		stateKey:  stateKey,
		lockTTL:   lockTTLSeconds,
	}
}

// IsLeader reports whether this node currently holds the leader lock.
func (c *Coordinator) IsLeader() bool {
	return c.isLeader.Load()
}

// Run starts the coordinator.  It continuously attempts to acquire the leader
// lock; on success it publishes ring state every lockTTL/2 seconds.  If the
// lock is held by another node, it watches and applies the state as a follower.
// Cancel ctx to stop.
func (c *Coordinator) Run(ctx context.Context) error {
	c.log.Info("consensus coordinator starting",
		zap.String("node_id", c.nodeID),
		zap.String("leader_key", c.leaderKey),
		zap.String("state_key", c.stateKey),
		zap.Int64("lock_ttl_s", c.lockTTL),
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		acquired, cancel, err := c.store.TryLock(ctx, c.leaderKey, c.lockTTL)
		if err != nil {
			c.log.Error("leader lock attempt failed", zap.Error(err))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
				continue
			}
		}

		if acquired {
			c.isLeader.Store(true)
			c.log.Info("became leader", zap.String("node_id", c.nodeID))
			c.runLeader(ctx, cancel)
			c.isLeader.Store(false)
		} else {
			// Follower: watch the state key and apply updates
			if err := c.runFollower(ctx); err != nil && ctx.Err() == nil {
				c.log.Error("follower watch error", zap.Error(err))
			}
		}
	}
}

// runLeader publishes ring state periodically while holding the leader lock.
// When ctx is cancelled or the lock is lost, it calls cancel() and returns.
func (c *Coordinator) runLeader(ctx context.Context, cancelLock context.CancelFunc) {
	defer cancelLock()

	publishInterval := time.Duration(c.lockTTL/2) * time.Second
	if publishInterval < time.Second {
		publishInterval = time.Second
	}
	ticker := time.NewTicker(publishInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.publishRingState(ctx); err != nil {
				c.log.Error("leader failed to publish ring state", zap.Error(err))
				return // give up leadership; retry from the top
			}
		}
	}
}

// publishRingState serializes the current ring state and writes it to etcd.
func (c *Coordinator) publishRingState(ctx context.Context) error {
	backends := c.ring.Backends()
	snaps := make([]BackendSnapshot, 0, len(backends))
	for _, id := range backends {
		b := c.ring.BackendInfo(id)
		if b == nil {
			continue
		}
		snaps = append(snaps, BackendSnapshot{
			ID:          b.ID,
			IP:          b.IP,
			Port:        b.Port,
			VnodeCount:  b.VnodeCount,
			CapacityMax: b.CapacityMax,
			Health:      b.Health,
			Stateful:    b.Stateful,
		})
	}

	version := time.Now().UnixNano()
	snap := RingStateSnapshot{
		Backends:  snaps,
		UpdatedAt: time.Now(),
		NodeID:    c.nodeID,
		Version:   version,
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal ring snapshot: %w", err)
	}
	if err := c.store.Put(ctx, c.stateKey, data); err != nil {
		return fmt.Errorf("put ring state: %w", err)
	}
	c.log.Debug("ring state published",
		zap.Int("backends", len(snaps)),
		zap.Int64("version", version),
	)
	return nil
}

// runFollower watches the state key and applies snapshots.
// Returns when ctx is cancelled or an unrecoverable watch error occurs.
func (c *Coordinator) runFollower(ctx context.Context) error {
	_, startRev, err := c.store.Get(ctx, c.stateKey)
	if err != nil {
		return err
	}

	ch, err := c.store.Watch(ctx, c.stateKey, startRev)
	if err != nil {
		return err
	}

	c.log.Info("follower watching ring state", zap.String("node_id", c.nodeID))

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil // channel closed; re-enter the election loop
			}
			if ev.Err != nil {
				return ev.Err
			}
			if err := c.applySnapshot(ev.Value); err != nil {
				c.log.Error("failed to apply ring state snapshot", zap.Error(err))
				// Non-fatal: keep watching; current ring state is still valid
			}
		}
	}
}

// applySnapshot deserializes and applies a ring state snapshot from the leader.
// Monotonically versioned: snapshots older than the last applied are silently
// discarded to handle etcd watch replay on reconnect.
func (c *Coordinator) applySnapshot(data []byte) error {
	var snap RingStateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("unmarshal ring snapshot: %w", err)
	}

	// Monotonic guard: discard older snapshots
	last := c.lastVersion.Load()
	if snap.Version <= last {
		c.log.Debug("discarding stale ring snapshot",
			zap.Int64("snapshot_version", snap.Version),
			zap.Int64("last_applied", last),
		)
		return nil
	}
	c.lastVersion.Store(snap.Version)

	// Apply the snapshot to the ring.  For each backend in the snapshot:
	//   • AddBackend if not known
	//   • SetHealth to match snapshot
	//   • SetVnodeCount to match snapshot
	for _, bs := range snap.Backends {
		c.ring.AddBackend(&ring.Backend{
			ID:          bs.ID,
			IP:          bs.IP,
			Port:        bs.Port,
			VnodeCount:  bs.VnodeCount,
			CapacityMax: bs.CapacityMax,
			Health:      bs.Health,
			Stateful:    bs.Stateful,
		})
		c.ring.SetHealth(bs.ID, bs.Health)
		c.ring.SetVnodeCount(bs.ID, bs.VnodeCount)
	}

	c.log.Info("applied ring state from leader",
		zap.String("leader_node", snap.NodeID),
		zap.Int("backends", len(snap.Backends)),
		zap.Int64("version", snap.Version),
		zap.Time("leader_updated_at", snap.UpdatedAt),
	)
	return nil
}

// ─── Status ───────────────────────────────────────────────────────────────────

// Status is a snapshot of coordinator state for the admin API.
type Status struct {
	NodeID      string `json:"node_id"`
	IsLeader    bool   `json:"is_leader"`
	LastVersion int64  `json:"last_applied_version"`
	LeaderKey   string `json:"leader_key"`
	StateKey    string `json:"state_key"`
	StoreType   string `json:"store_type"` // "memory" | "etcd"
}

// GetStatus returns a point-in-time snapshot of the coordinator's state.
func (c *Coordinator) GetStatus() Status {
	storeType := "etcd"
	if _, ok := c.store.(*MemStateStore); ok {
		storeType = "memory"
	}
	return Status{
		NodeID:      c.nodeID,
		IsLeader:    c.isLeader.Load(),
		LastVersion: c.lastVersion.Load(),
		LeaderKey:   c.leaderKey,
		StateKey:    c.stateKey,
		StoreType:   storeType,
	}
}
