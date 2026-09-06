package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/ring"
)

func newTestRing(t *testing.T) *ring.Manager {
	t.Helper()
	rm, err := ring.NewManager(config.RingConfig{AdjustEveryN: 100, AdjustThreshold: 1.30}, zap.NewNop())
	if err != nil {
		t.Fatalf("ring.NewManager: %v", err)
	}
	return rm
}

func TestCheckMarksBackendDownAfterFailThreshold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	rm := newTestRing(t)
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})

	c := NewChecker(config.HealthConfig{FailThreshold: 3}, rm, zap.NewNop())
	c.Register(1, srv.URL)

	ep, _ := c.endpoints.Load(uint32(1))
	endpoint := ep.(*BackendEndpoint)

	ctx := context.Background()
	c.check(ctx, endpoint)
	c.check(ctx, endpoint)
	if b := rm.BackendInfo(1); !b.Health {
		t.Fatalf("backend should still be healthy after 2 failures (threshold 3)")
	}
	c.check(ctx, endpoint)
	if b := rm.BackendInfo(1); b.Health {
		t.Fatalf("backend should be marked DOWN after reaching fail threshold")
	}
}

func TestCheckRecoversBackendOnSuccessAfterFailure(t *testing.T) {
	failing := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	rm := newTestRing(t)
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})

	c := NewChecker(config.HealthConfig{FailThreshold: 1}, rm, zap.NewNop())
	c.Register(1, srv.URL)
	ep, _ := c.endpoints.Load(uint32(1))
	endpoint := ep.(*BackendEndpoint)

	ctx := context.Background()
	c.check(ctx, endpoint) // 1 failure hits threshold of 1
	if b := rm.BackendInfo(1); b.Health {
		t.Fatalf("backend should be DOWN after a single failure at threshold 1")
	}

	failing = false
	c.check(ctx, endpoint)
	if b := rm.BackendInfo(1); !b.Health {
		t.Fatalf("backend should be UP again after a successful probe")
	}
}

func TestMarkPassTriggersSlowStartAtExactlyMinSuccesses(t *testing.T) {
	rm := newTestRing(t)
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})

	c := NewChecker(config.HealthConfig{FailThreshold: 3, MinSuccessesBeforeRestore: 3}, rm, zap.NewNop())
	c.Register(1, "unused")
	ep, _ := c.endpoints.Load(uint32(1))
	endpoint := ep.(*BackendEndpoint)

	var recoveredCount int
	var recoveredID uint32
	c.SetRecoveryCallback(func(id uint32) {
		recoveredCount++
		recoveredID = id
	})

	c.markPass(endpoint)
	c.markPass(endpoint)
	if recoveredCount != 0 {
		t.Fatalf("recovery callback fired too early, after %d successes", 2)
	}
	c.markPass(endpoint) // 3rd consecutive success == MinSuccessesBeforeRestore
	if recoveredCount != 1 || recoveredID != 1 {
		t.Fatalf("expected recovery callback exactly once for backend 1, got count=%d id=%d", recoveredCount, recoveredID)
	}

	c.markPass(endpoint) // 4th success must not re-fire
	if recoveredCount != 1 {
		t.Fatalf("recovery callback should fire exactly once, fired %d times", recoveredCount)
	}
}

func TestMarkFailResetsConsecutiveSuccessCounter(t *testing.T) {
	rm := newTestRing(t)
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})

	c := NewChecker(config.HealthConfig{FailThreshold: 100, MinSuccessesBeforeRestore: 2}, rm, zap.NewNop())
	c.Register(1, "unused")
	ep, _ := c.endpoints.Load(uint32(1))
	endpoint := ep.(*BackendEndpoint)

	var recovered bool
	c.SetRecoveryCallback(func(uint32) { recovered = true })

	c.markPass(endpoint)                   // 1 consecutive success
	c.markFail(endpoint, context.Canceled) // resets to 0, does not hit FailThreshold (100)
	c.markPass(endpoint)                   // 1 again, not 2
	if recovered {
		t.Fatalf("recovery should not fire — failure must reset the consecutive-success counter")
	}
	c.markPass(endpoint) // now 2 consecutive since the reset
	if !recovered {
		t.Fatalf("recovery should fire once 2 fresh consecutive successes are reached")
	}
}

func TestMarkPassDefaultsMinSuccessesTo60(t *testing.T) {
	rm := newTestRing(t)
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})

	c := NewChecker(config.HealthConfig{FailThreshold: 3}, rm, zap.NewNop()) // MinSuccessesBeforeRestore: 0 (unset)
	c.Register(1, "unused")
	ep, _ := c.endpoints.Load(uint32(1))
	endpoint := ep.(*BackendEndpoint)

	var recovered bool
	c.SetRecoveryCallback(func(uint32) { recovered = true })

	for i := 0; i < 59; i++ {
		c.markPass(endpoint)
	}
	if recovered {
		t.Fatalf("recovery fired before the default threshold of 60 was reached")
	}
	c.markPass(endpoint) // 60th
	if !recovered {
		t.Fatalf("recovery should fire at the default threshold of 60 consecutive successes")
	}
}

func TestDeregisterRemovesEndpoint(t *testing.T) {
	rm := newTestRing(t)
	c := NewChecker(config.HealthConfig{}, rm, zap.NewNop())
	c.Register(1, "http://example.invalid")
	if _, ok := c.endpoints.Load(uint32(1)); !ok {
		t.Fatalf("expected endpoint 1 to be registered")
	}
	c.Deregister(1)
	if _, ok := c.endpoints.Load(uint32(1)); ok {
		t.Fatalf("expected endpoint 1 to be removed after Deregister")
	}
}
