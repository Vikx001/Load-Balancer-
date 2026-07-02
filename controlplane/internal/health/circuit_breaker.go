package health

import (
	"context"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"go.uber.org/zap"
)

// ─── WHY A GO-SIDE CIRCUIT BREAKER ───────────────────────────────────────────
// The eBPF layer trips the circuit (OPEN) in ~50ms by counting consecutive
// 5xx responses at kernel speed.  But the kernel cannot implement time-based
// state transitions — it has no sleep and no timers in BPF_PROG_TYPE_SOCK_OPS.
//
// The Go CircuitBreakerManager handles the time-based side:
//   OPEN  → HALF_OPEN: after openTimeoutS seconds (default 10s)
//   HALF_OPEN → CLOSED:  when the health checker confirms a successful probe
//   HALF_OPEN → OPEN:    when the probe fails (re-trip)
//
// It also handles the case where the eBPF program itself resets a HALF_OPEN
// circuit to CLOSED on a successful request (written by metrics_collector.bpf.c).
// In that case, the manager's poll will simply see CLOSED and do nothing.
//
// Detection latency comparison:
//   Without circuit breaker: 2s poll × 3 fails = 6 seconds
//   With circuit breaker:    5 × ~10ms response = ~50ms kernel detection
//   Control plane pick-up:   50ms + 1s poll = ~1.05s worst-case OPEN→route-skip

const (
	defaultOpenTimeoutS = 10 // seconds before OPEN transitions to HALF_OPEN
	managerPollInterval = time.Second
)

// circuitState mirrors the eBPF constants in omega_maps.h
type circuitState uint32

const (
	circuitClosed   circuitState = 0
	circuitOpen     circuitState = 1
	circuitHalfOpen circuitState = 2
)

// backendCircuit tracks the per-backend state as seen by the manager.
type backendCircuit struct {
	state      circuitState
	openedAt   time.Time // when state last transitioned to OPEN
	halfOpenAt time.Time // when state last transitioned to HALF_OPEN
}

// CircuitBreakerManager reads the circuit_state_map from eBPF and manages
// time-based state transitions (OPEN→HALF_OPEN) and health-checker triggered
// resets (HALF_OPEN→CLOSED / HALF_OPEN→OPEN).
type CircuitBreakerManager struct {
	mu          sync.Mutex
	log         *zap.Logger
	states      map[uint32]*backendCircuit // backendID → state
	pinPath     string                     // path to BPF pin directory
	openTimeout time.Duration

	// notifyClosed is called by the manager when a circuit moves to CLOSED.
	// Used by the health checker to know when to begin slow-start.
	notifyClosed func(backendID uint32)
}

// NewCircuitBreakerManager creates a CircuitBreakerManager.
// pinPath: directory where BPF maps are pinned (e.g. /sys/fs/bpf/omega).
// notifyClosed: optional callback, called when a circuit transitions to CLOSED.
func NewCircuitBreakerManager(pinPath string, log *zap.Logger, notifyClosed func(uint32)) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		log:          log,
		states:       make(map[uint32]*backendCircuit),
		pinPath:      pinPath,
		openTimeout:  defaultOpenTimeoutS * time.Second,
		notifyClosed: notifyClosed,
	}
}

// Run starts the circuit breaker polling loop.  Call in a goroutine; cancel
// the context to stop.
func (cb *CircuitBreakerManager) Run(ctx context.Context) error {
	ticker := time.NewTicker(managerPollInterval)
	defer ticker.Stop()

	cb.log.Info("circuit breaker manager started",
		zap.String("pin_path", cb.pinPath),
		zap.Duration("open_timeout", cb.openTimeout),
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := cb.tick(); err != nil {
				cb.log.Error("circuit breaker tick error", zap.Error(err))
				// Non-fatal: continue polling; eBPF kernel-side protection stays active
			}
		}
	}
}

// tick reads the current circuit states from eBPF and drives transitions.
func (cb *CircuitBreakerManager) tick() error {
	csMap, err := ebpf.LoadPinnedMap(cb.pinPath+"/circuit_state_map", nil)
	if err != nil {
		// Map not yet pinned (daemon just started) — not an error
		return nil
	}
	defer csMap.Close()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	var backendID uint32
	var state uint32

	iter := csMap.Iterate()
	for iter.Next(&backendID, &state) {
		cs := circuitState(state)
		bc, exists := cb.states[backendID]
		if !exists {
			bc = &backendCircuit{}
			cb.states[backendID] = bc
		}

		switch cs {
		case circuitOpen:
			if bc.state != circuitOpen {
				// Newly tripped by the kernel
				bc.state = circuitOpen
				bc.openedAt = now
				cb.log.Warn("circuit OPEN: backend has consecutive 5xx errors",
					zap.Uint32("backend_id", backendID),
					zap.Duration("open_timeout", cb.openTimeout),
					zap.String("action", "backend excluded from routing; will probe after timeout"),
				)
			} else if now.Sub(bc.openedAt) >= cb.openTimeout {
				// Time to allow a probe
				if err := cb.writeState(csMap, backendID, circuitHalfOpen); err != nil {
					cb.log.Error("failed to write HALF_OPEN", zap.Error(err))
					continue
				}
				bc.state = circuitHalfOpen
				bc.halfOpenAt = now
				cb.log.Info("circuit HALF_OPEN: allowing probe request",
					zap.Uint32("backend_id", backendID),
				)
			}

		case circuitHalfOpen:
			if bc.state != circuitHalfOpen {
				bc.state = circuitHalfOpen
				bc.halfOpenAt = now
			}
			// If the eBPF metrics_collector already reset it to CLOSED via a
			// successful probe, we'll see circuitClosed on the next tick.
			// No action needed here — wait for the probe result.

		case circuitClosed:
			if bc.state == circuitHalfOpen || bc.state == circuitOpen {
				// Recovered!
				prevState := bc.state
				bc.state = circuitClosed
				cb.log.Info("circuit CLOSED: backend recovered",
					zap.Uint32("backend_id", backendID),
					zap.String("previous_state", circuitStateLabel(prevState)),
				)
				if cb.notifyClosed != nil {
					go cb.notifyClosed(backendID) // begin slow-start
				}
			}
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	return nil
}

// NotifyProbeResult is called by the health checker to report the outcome of a
// HALF_OPEN probe request.  On failure, the circuit is re-opened immediately.
func (cb *CircuitBreakerManager) NotifyProbeResult(backendID uint32, success bool) {
	if success {
		return // metrics_collector or the next tick will confirm CLOSED
	}
	// Probe failed: re-open the circuit immediately
	cb.mu.Lock()
	defer cb.mu.Unlock()

	csMap, err := ebpf.LoadPinnedMap(cb.pinPath+"/circuit_state_map", nil)
	if err != nil {
		cb.log.Error("failed to open circuit_state_map for re-trip", zap.Error(err))
		return
	}
	defer csMap.Close()

	if err := cb.writeState(csMap, backendID, circuitOpen); err != nil {
		cb.log.Error("failed to re-open circuit", zap.Error(err))
		return
	}
	if bc, ok := cb.states[backendID]; ok {
		bc.state = circuitOpen
		bc.openedAt = time.Now()
	}
	cb.log.Warn("circuit re-OPEN: half-open probe failed",
		zap.Uint32("backend_id", backendID),
	)
}

// State returns the current known circuit state for a backend.
// Returns circuitClosed if the backend has no recorded state (unknown = closed).
func (cb *CircuitBreakerManager) State(backendID uint32) circuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if bc, ok := cb.states[backendID]; ok {
		return bc.state
	}
	return circuitClosed
}

func (cb *CircuitBreakerManager) writeState(m *ebpf.Map, backendID uint32, state circuitState) error {
	v := uint32(state)
	return m.Update(&backendID, &v, ebpf.UpdateAny)
}

func circuitStateLabel(s circuitState) string {
	switch s {
	case circuitClosed:
		return "CLOSED"
	case circuitOpen:
		return "OPEN"
	case circuitHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}
