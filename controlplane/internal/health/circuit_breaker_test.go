package health

import (
	"testing"

	"go.uber.org/zap"
)

func TestCircuitBreakerStateDefaultsToClosed(t *testing.T) {
	cb := NewCircuitBreakerManager("/nonexistent", zap.NewNop(), nil)
	if s := cb.State(42); s != circuitClosed {
		t.Fatalf("expected unknown backend to report CLOSED, got %v", circuitStateLabel(s))
	}
}

func TestCircuitStateLabel(t *testing.T) {
	cases := []struct {
		state circuitState
		want  string
	}{
		{circuitClosed, "CLOSED"},
		{circuitOpen, "OPEN"},
		{circuitHalfOpen, "HALF_OPEN"},
		{circuitState(99), "UNKNOWN"},
	}
	for _, c := range cases {
		if got := circuitStateLabel(c.state); got != c.want {
			t.Errorf("circuitStateLabel(%d) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestNotifyProbeResultSuccessIsNoop(t *testing.T) {
	cb := NewCircuitBreakerManager("/nonexistent", zap.NewNop(), nil)
	// Seed a known state directly (no eBPF map available in this test env).
	cb.states[7] = &backendCircuit{state: circuitHalfOpen}

	cb.NotifyProbeResult(7, true)

	if s := cb.State(7); s != circuitHalfOpen {
		t.Fatalf("a successful probe result must not change state itself (the next tick/eBPF confirms CLOSED); got %v", circuitStateLabel(s))
	}
}

func TestNotifyProbeResultFailureDoesNotPanicWithoutPinnedMap(t *testing.T) {
	cb := NewCircuitBreakerManager("/nonexistent", zap.NewNop(), nil)
	cb.states[7] = &backendCircuit{state: circuitHalfOpen}

	// No pinned eBPF map exists at this path — must degrade gracefully (log
	// and return) rather than panic, since this runs on every failed probe.
	cb.NotifyProbeResult(7, false)
}
