package consensus

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/ring"
)

func newTestCoordinator(t *testing.T) (*Coordinator, *ring.Manager, *MemStateStore) {
	t.Helper()
	rm, err := ring.NewManager(config.RingConfig{AdjustEveryN: 100, AdjustThreshold: 1.30}, zap.NewNop())
	if err != nil {
		t.Fatalf("ring.NewManager: %v", err)
	}
	store := NewMemStateStore()
	c := NewCoordinator(store, "node-1", "/omega-lb/leader", "/omega-lb/ring-state", 10, rm, zap.NewNop())
	return c, rm, store
}

func TestGetStatusReportsMemoryStoreType(t *testing.T) {
	c, _, _ := newTestCoordinator(t)
	status := c.GetStatus()
	if status.StoreType != "memory" {
		t.Fatalf("expected store_type=memory for MemStateStore, got %q", status.StoreType)
	}
	if status.NodeID != "node-1" {
		t.Fatalf("expected node_id=node-1, got %q", status.NodeID)
	}
	if status.IsLeader {
		t.Fatalf("coordinator should not report leadership before Run() acquires the lock")
	}
}

func TestNewCoordinatorDefaultsInvalidTTL(t *testing.T) {
	rm, _ := ring.NewManager(config.RingConfig{AdjustEveryN: 100, AdjustThreshold: 1.30}, zap.NewNop())
	c := NewCoordinator(NewMemStateStore(), "node-1", "leader", "state", 0, rm, zap.NewNop())
	if c.lockTTL != 10 {
		t.Fatalf("expected non-positive TTL to default to 10s, got %d", c.lockTTL)
	}
}

func TestPublishRingStateRoundTripsThroughStore(t *testing.T) {
	c, rm, store := newTestCoordinator(t)
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, VnodeCount: 150, CapacityMax: 1000})
	rm.AddBackend(&ring.Backend{ID: 2, Health: false, VnodeCount: 150, CapacityMax: 1000})

	ctx := context.Background()
	if err := c.publishRingState(ctx); err != nil {
		t.Fatalf("publishRingState: %v", err)
	}

	raw, _, err := store.Get(ctx, "/omega-lb/ring-state")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var snap RingStateSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal published snapshot: %v", err)
	}
	if snap.NodeID != "node-1" || len(snap.Backends) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestApplySnapshotAddsBackendsAndSyncsState(t *testing.T) {
	c, rm, _ := newTestCoordinator(t)

	snap := RingStateSnapshot{
		Version: 100,
		NodeID:  "leader-node",
		Backends: []BackendSnapshot{
			{ID: 5, VnodeCount: 80, CapacityMax: 500, Health: true},
		},
	}
	data, _ := json.Marshal(snap)

	if err := c.applySnapshot(data); err != nil {
		t.Fatalf("applySnapshot: %v", err)
	}

	b := rm.BackendInfo(5)
	if b == nil {
		t.Fatalf("expected backend 5 to be added to the local ring")
	}
	if b.VnodeCount != 80 || !b.Health {
		t.Fatalf("backend 5 state not applied correctly: %+v", b)
	}
	if c.lastVersion.Load() != 100 {
		t.Fatalf("expected lastVersion=100, got %d", c.lastVersion.Load())
	}
}

func TestApplySnapshotDiscardsStaleVersion(t *testing.T) {
	c, rm, _ := newTestCoordinator(t)

	newer := RingStateSnapshot{Version: 200, Backends: []BackendSnapshot{{ID: 1, VnodeCount: 150, Health: true}}}
	data, _ := json.Marshal(newer)
	if err := c.applySnapshot(data); err != nil {
		t.Fatalf("applySnapshot(newer): %v", err)
	}

	// An older snapshot that tries to change backend 1's vnode count must be ignored.
	stale := RingStateSnapshot{Version: 100, Backends: []BackendSnapshot{{ID: 1, VnodeCount: 1, Health: false}}}
	data, _ = json.Marshal(stale)
	if err := c.applySnapshot(data); err != nil {
		t.Fatalf("applySnapshot(stale): %v", err)
	}

	if c.lastVersion.Load() != 200 {
		t.Fatalf("stale snapshot must not move lastVersion backward, got %d", c.lastVersion.Load())
	}
	b := rm.BackendInfo(1)
	if b.VnodeCount != 150 || !b.Health {
		t.Fatalf("stale snapshot must not have been applied, got %+v", b)
	}
}

func TestApplySnapshotRejectsMalformedJSON(t *testing.T) {
	c, _, _ := newTestCoordinator(t)
	if err := c.applySnapshot([]byte("not json")); err == nil {
		t.Fatal("expected an error for malformed snapshot JSON")
	}
}
