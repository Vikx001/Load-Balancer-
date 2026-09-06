package ring

import (
	"testing"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(config.RingConfig{AdjustEveryN: 100, AdjustThreshold: 1.30}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestSetDrainingExcludesFromRouting(t *testing.T) {
	m := newTestManager(t)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 2, Health: true, CapacityMax: 1000})

	if !m.SetDraining(1, true) {
		t.Fatalf("SetDraining(1, true) returned false for a known backend")
	}

	for i := 0; i < 200; i++ {
		id, err := m.Route(uint32(i))
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if id == 1 {
			t.Fatalf("Route selected draining backend 1")
		}
	}

	if b := m.BackendInfo(1); b == nil || !b.Draining {
		t.Fatalf("backend 1 should report Draining=true, got %+v", b)
	}
	if b := m.BackendInfo(1); b == nil || !b.Health {
		t.Fatalf("draining must not flip Health — SetHealth and SetDraining are independent")
	}
}

func TestSetDrainingSoleBackendRejectsRouting(t *testing.T) {
	m := newTestManager(t)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})
	m.SetDraining(1, true)

	if _, err := m.Route(42); err == nil {
		t.Fatalf("expected Route to fail with no non-draining backends, got nil error")
	}
}

func TestSetDrainingUnknownBackendReturnsFalse(t *testing.T) {
	m := newTestManager(t)
	if m.SetDraining(999, true) {
		t.Fatalf("SetDraining on an unregistered backend should return false")
	}
}

func TestSetDrainingAllDrainsEveryBackend(t *testing.T) {
	m := newTestManager(t)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 2, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 3, Health: true, CapacityMax: 1000})

	if n := m.SetDrainingAll(true); n != 3 {
		t.Fatalf("expected SetDrainingAll to report 3 backends affected, got %d", n)
	}

	for _, id := range []uint32{1, 2, 3} {
		b := m.BackendInfo(id)
		if b == nil || !b.Draining {
			t.Fatalf("backend %d should be draining after SetDrainingAll(true), got %+v", id, b)
		}
	}
	if _, err := m.Route(1); err == nil {
		t.Fatalf("expected Route to fail with every backend drained")
	}

	if n := m.SetDrainingAll(false); n != 3 {
		t.Fatalf("expected SetDrainingAll(false) to report 3 backends affected, got %d", n)
	}
	if _, err := m.Route(1); err != nil {
		t.Fatalf("Route after SetDrainingAll(false): %v", err)
	}
}

func TestSetDrainingCanBeCancelled(t *testing.T) {
	m := newTestManager(t)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})

	m.SetDraining(1, true)
	if _, err := m.Route(1); err == nil {
		t.Fatalf("expected Route to fail while backend 1 is draining")
	}

	m.SetDraining(1, false)
	id, err := m.Route(1)
	if err != nil {
		t.Fatalf("Route after undrain: %v", err)
	}
	if id != 1 {
		t.Fatalf("expected backend 1 to be routable again, got %d", id)
	}
}
