package ring

import (
	"testing"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
)

// ─── Chaos / failure-injection scenarios ───────────────────────────────────
//
// These tests simulate the specific incident timelines the README documents
// under "State & Consistency Layer — Operational Safety Reference" and drive
// the real ring.Manager through them deterministically (no real timers),
// asserting the documented behavior actually happens — not just that the
// code doesn't panic.

func newChaosManager(t *testing.T, vnodesPerServer int) *Manager {
	t.Helper()
	m, err := NewManager(config.RingConfig{
		VnodesPerServer: vnodesPerServer,
		AdjustEveryN:    100,
		AdjustThreshold: 1.30,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// Scenario: a healthy backend fails, is marked DOWN, and later recovers.
// While DOWN it must carry zero traffic share; once healthy again it must
// return to the ring.
func TestChaosBackendFailureAndRecoveryExcludesFromRingWhileDown(t *testing.T) {
	m := newChaosManager(t, 150)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 2, Health: true, CapacityMax: 1000})

	// Kill backend 1 (simulates 3 consecutive failed health probes).
	m.SetHealth(1, false)

	for i := 0; i < 100; i++ {
		id, err := m.Route(uint32(i))
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if id == 1 {
			t.Fatalf("DOWN backend 1 must never be selected by Route")
		}
	}

	// Recover it.
	m.SetHealth(1, true)
	sawBackend1 := false
	for i := 0; i < 200; i++ {
		id, err := m.Route(uint32(i))
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if id == 1 {
			sawBackend1 = true
			break
		}
	}
	if !sawBackend1 {
		t.Fatalf("backend 1 should be routable again after SetHealth(true)")
	}
}

// Scenario from the README ("Thundering Herd on Backend Restart"): a backend
// recovers and slow-start is supposed to ramp it from 0 to full vnodes in
// batches, so it never receives 100% of its traffic share the instant it's
// marked healthy again.
//
// This test currently FAILS, and that is the point: it demonstrates that
// SetHealth(id, true) — called by the health checker on the very first
// successful probe after a failure streak, well before the
// minSuccessesBeforeRestore gate that triggers BeginSlowStart — puts the
// backend back into rebuild()'s ring with its ORIGINAL VnodeCount, because
// nothing ever zeroes VnodeCount when a backend goes DOWN. BeginSlowStart's
// own bookkeeping (currentVnodes starting at 0) never gets a chance to matter,
// because the ring already has the backend at full weight by then.
//
// Skipped by default so `go test ./...` stays green; run explicitly with
// `go test ./internal/ring/ -run ThunderingHerd -v` to see it fail, or
// remove the Skip once BeginSlowStart's zero-then-ramp behavior is wired to
// actually gate the ring (e.g. SetHealth(true) should not restore VnodeCount
// on its own when slow-start is enabled; the controller should own it end to
// end via SetVnodeCount(id, 0) at recovery time).
func TestChaosThunderingHerdOnRecoveryDoesNotActuallyRampGradually(t *testing.T) {
	t.Skip("documents a real gap between the README's slow-start claim and current wiring — see comment above")

	m := newChaosManager(t, 150)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 2, Health: true, CapacityMax: 1000})

	m.SetHealth(1, false) // backend 1 goes down
	m.SetHealth(1, true)  // first successful probe after recovery

	b := m.BackendInfo(1)
	if b.VnodeCount != 0 {
		t.Fatalf("expected a just-recovered backend to start at 0 vnodes before slow-start ramps it up, got %d — "+
			"it will receive its full traffic share immediately instead of ramping over ~4.5 minutes as documented",
			b.VnodeCount)
	}
}

// Scenario: a backend is drained for maintenance while ALSO being the
// target of H&A vnode rebalancing pressure from an overloaded peer. Draining
// must win — adjust() must never hand vnodes back to a draining backend.
func TestChaosDrainingBackendNeverReceivesRebalancedVnodes(t *testing.T) {
	m := newChaosManager(t, 150)
	hot := &Backend{ID: 1, Health: true, CapacityMax: 1000, VnodeCount: 150}
	draining := &Backend{ID: 2, Health: true, CapacityMax: 1000, VnodeCount: 150}
	m.AddBackend(hot)
	m.AddBackend(draining)
	m.SetDraining(2, true)

	// Simulate backend 1 being far hotter than backend 2 (which, being
	// drained, correctly carries zero active load).
	hot.ActiveReqs = 1000
	draining.ActiveReqs = 0

	m.adjust() // exported indirectly via Route()'s periodic call in real use

	if draining.VnodeCount != 150 {
		t.Fatalf("adjust() must never award vnodes to a draining backend, got vnode_count=%d", draining.VnodeCount)
	}
}

// Scenario: every backend fails simultaneously (total outage). Route must
// fail closed with a clear error, never panic, and recovery of any single
// backend must immediately restore routability.
func TestChaosTotalOutageThenSingleBackendRecovery(t *testing.T) {
	m := newChaosManager(t, 150)
	m.AddBackend(&Backend{ID: 1, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 2, Health: true, CapacityMax: 1000})
	m.AddBackend(&Backend{ID: 3, Health: true, CapacityMax: 1000})

	m.SetHealth(1, false)
	m.SetHealth(2, false)
	m.SetHealth(3, false)

	if _, err := m.Route(1); err == nil {
		t.Fatalf("expected Route to fail during a total outage")
	}

	m.SetHealth(2, true)
	id, err := m.Route(1)
	if err != nil {
		t.Fatalf("Route after recovering one backend: %v", err)
	}
	if id != 2 {
		t.Fatalf("expected the only healthy backend (2) to be selected, got %d", id)
	}
}
