// Package rl implements Layer 2 (PPO+CBF) and Layer 3 (KAN actor) control.
package rl

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/metrics"
	"github.com/omega-lb/omega-lb/internal/ring"
)

// AgentMode controls how the RL agent influences routing decisions.
//
// ─── WHY MODE SWITCHING IS AN OPERATIONAL NECESSITY ──────────────────────────
// During an incident, SREs need to isolate whether the RL agent is contributing
// to the problem without taking down the entire load balancer.  Without modes:
//   - No way to test "is this the RL agent's fault?" (→ must kill the process)
//   - No way to apply static weights while investigating (→ must redeploy)
//   - Runbook says "kill omega-lb" which interrupts all traffic
//
// With modes, the operator can:
//
//	$ curl -XPOST http://localhost:9000/admin/mode -d '{"mode":"ASSISTED"}'  # skip KAN
//	$ curl -XPOST http://localhost:9000/admin/mode -d '{"mode":"MANUAL","weights":[0.5,0.3,0.2]}'
//	$ curl -XPOST http://localhost:9000/admin/mode -d '{"mode":"AUTO"}' # restore RL
type AgentMode int

const (
	// ModeAuto: full RL control — KAN actor + CBF + oscillation gate (default).
	ModeAuto AgentMode = iota
	// ModeAssisted: H&A ring only; KAN inference is skipped; CBF still protects.
	// Use during incidents to isolate whether the RL agent is causing problems.
	ModeAssisted
	// ModeManual: operator-specified static weights; no KAN, no ring-auto-adjust.
	// Use when you know exactly what distribution you want and need to hold it.
	ModeManual
)

// Agent is the main RL control loop: observe → infer → project → act.
type Agent struct {
	cfg   config.RLConfig
	log   *zap.Logger
	kan   *KANActor
	cbf   *CBFProjector
	ring  *ring.Manager
	ood   *OODDetector
	prevW []float64
	mu    sync.Mutex

	// mode controls whether KAN inference is used (see AgentMode above).
	mode          AgentMode
	manualWeights []float64 // used when mode == ModeManual

	// modelVersion tracks the currently loaded ONNX model version.
	modelVersion string
	modelMu      sync.RWMutex // guards kan + modelVersion during hot-reload

	// Oscillation gate state.
	prevAppliedW      []float64
	pendingW          []float64
	pendingAgreements int
}

// NewAgent constructs the RL agent.
func NewAgent(cfg config.RLConfig, kan *KANActor, cbf *CBFProjector,
	rm *ring.Manager, log *zap.Logger) (*Agent, error) {
	// OOD detector dimensioned for the standard state vector
	// (num_backends × 8 + 4 global features; use 64 as a conservative upper bound
	// — the detector ignores extra dimensions gracefully via length check).
	ood := NewOODDetector(64, 3.0, log)
	return &Agent{
		cfg:          cfg,
		log:          log,
		kan:          kan,
		cbf:          cbf,
		ring:         rm,
		ood:          ood,
		mode:         ModeAuto,
		modelVersion: cfg.ModelVersion,
	}, nil
}

// Run is the main RL control loop, executing every cfg.StepIntervalMs.
func (a *Agent) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(a.cfg.StepIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.step(); err != nil {
				a.log.Warn("RL step error", zap.Error(err))
			}
		}
	}
}

// step: single RL decision cycle.
func (a *Agent) step() error {
	// 1. Collect state from metrics
	state, backends := collectState()
	if len(backends) == 0 {
		return nil
	}

	// 2. OOD detection — must happen before inference so we can adjust model
	//    weight in the smoothing formula.  Also feeds the running distribution
	//    estimate so the detector improves over time.
	oodScore := a.ood.Score(state)
	modelWeight := a.ood.ActionWeight(oodScore, 1.0-a.cfg.ActionSmoothing)
	if a.ood.IsOOD(state) {
		a.ood.LogOODEvent(oodScore, state)
	} else {
		// Only update the training distribution estimate when the state is
		// in-distribution; OOD states would corrupt the running statistics.
		a.ood.Update(state)
	}

	// 3. KAN actor inference (with timeout fallback)
	var rawW []float64
	var err error

	// Mode check: in ModeAssisted and ModeManual we bypass KAN inference.
	// This lets SREs isolate RL from an incident without killing the daemon.
	switch a.GetMode() {
	case ModeManual:
		a.mu.Lock()
		if len(a.manualWeights) == len(backends) {
			rawW = make([]float64, len(a.manualWeights))
			copy(rawW, a.manualWeights)
		} else {
			rawW = uniformWeights(len(backends))
		}
		a.mu.Unlock()
	case ModeAssisted:
		rawW = uniformWeights(len(backends)) // H&A ring controls distribution
	default: // ModeAuto
		a.modelMu.RLock()
		kan := a.kan
		a.modelMu.RUnlock()
		if kan != nil {
			ctx, cancel := context.WithTimeout(context.Background(),
				time.Duration(a.cfg.InferenceTimeoutMs)*time.Millisecond)
			rawW, err = kan.Infer(ctx, state)
			cancel()
			if err != nil {
				a.log.Warn("KAN inference failed, using uniform weights", zap.Error(err))
				rawW = uniformWeights(len(backends))
			}
		} else {
			rawW = uniformWeights(len(backends))
		}
	}

	// 4. CBF safety projection.
	//
	// NEVER use rawW directly on CBF failure.  CBFProjector.Project() implements
	// the 3-tier fallback internally (tier-1: last safe weights; tier-2: freeze).
	// On any failure, the returned weights are the safest available alternative.
	cbfBackends := make([]CBFBackendState, len(backends))
	for i, id := range backends {
		cbfBackends[i] = CBFBackendState{ID: id}
	}
	safeW, cbfErr := a.cbf.Project(rawW, state, cbfBackends)
	if cbfErr != nil {
		// cbf.Project already logged the error at Error level with tier info.
		// Log additional context at Debug so production logs aren't spammed.
		a.log.Debug("CBF fallback active", zap.Error(cbfErr))
	}
	if a.cbf.IsFrozen() {
		// Tier-2: CBF has failed 3+ consecutive times.  Do NOT update the ring.
		// The ring continues serving traffic on the last successfully applied
		// weights.  This is the correct fail-safe: the H&A ring is already
		// running with the most recent safe distribution.
		a.log.Error("CBF frozen — skipping ring update; ring-only routing active until operator resolves constraint failure")
		return fmt.Errorf("CBF projector is frozen; RL control loop suspended")
	}

	// 5. Action smoothing: w_new = α_ood×w_model + (1−α_ood)×w_previous
	//
	// When the state is OOD, modelWeight is reduced toward 0, falling back to
	// the previous (in-distribution) weights.  This prevents the agent from
	// making large confident-looking moves based on poor value function estimates.
	a.mu.Lock()
	if a.prevW == nil || len(a.prevW) != len(safeW) {
		a.prevW = safeW
	} else {
		for i := range safeW {
			safeW[i] = modelWeight*safeW[i] + (1-modelWeight)*a.prevW[i]
		}
		a.prevW = safeW
	}
	a.mu.Unlock()

	// 6. Oscillation gate: require two consecutive model steps agreeing before
	//    applying any weight vector that would shift total vnode count by >10%.
	if !a.shouldApplyWeights(safeW, len(backends)) {
		a.log.Debug("oscillation gate: holding ring update pending second confirmation",
			zap.Float64s("candidate_weights", safeW),
		)
		return nil
	}

	// 7. Apply weights to H&A ring (update vnode counts)
	a.mu.Lock()
	a.prevAppliedW = make([]float64, len(safeW))
	copy(a.prevAppliedW, safeW)
	a.mu.Unlock()
	applyWeightsToRing(a.ring, backends, safeW)

	a.log.Debug("RL step complete",
		zap.Float64s("weights", safeW),
		zap.Float64("ood_score", oodScore),
		zap.Float64("model_weight", modelWeight),
		zap.Int("backends", len(backends)),
		zap.String("model_version", a.modelVersion),
		zap.Int("mode", int(a.GetMode())),
	)
	return nil
}

// GetMode returns the current operating mode.
func (a *Agent) GetMode() AgentMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// SetMode switches the agent between AUTO, ASSISTED, and MANUAL modes.
// This is called by the admin HTTP server on POST /admin/mode.
func (a *Agent) SetMode(m AgentMode, manualWeights []float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	prev := a.mode
	a.mode = m
	if m == ModeManual && len(manualWeights) > 0 {
		a.manualWeights = make([]float64, len(manualWeights))
		copy(a.manualWeights, manualWeights)
	}
	a.log.Info("agent mode changed",
		zap.Int("from", int(prev)),
		zap.Int("to", int(m)),
	)
}

// GetModelVersion returns the currently loaded model version.
func (a *Agent) GetModelVersion() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.modelVersion
}

// HotReload swaps the KAN actor to a new ONNX model without restarting the
// daemon or interrupting traffic.  The old model is released after the swap.
//
// Thread-safety: the swap is guarded by modelMu.  The inference goroutine
// holds modelMu.RLock() during Infer(), so this blocks for at most one
// inference cycle (≤ cfg.InferenceTimeoutMs) before completing the swap.
func (a *Agent) HotReload(newModelPath, newVersion string) error {
	newKAN, err := NewKANActor(newModelPath, a.log)
	if err != nil {
		return fmt.Errorf("hot-reload: load new model %s: %w", newModelPath, err)
	}
	a.modelMu.Lock()
	a.kan = newKAN
	a.modelVersion = newVersion
	a.modelMu.Unlock()
	a.log.Info("KAN actor hot-reloaded",
		zap.String("version", newVersion),
		zap.String("path", newModelPath),
	)
	return nil
}

// shouldApplyWeights implements the oscillation gate.
//
// If the candidate weights would change the total vnode count by more than 10%
// relative to the last applied weights, we require the same shift to be
// confirmed by two consecutive model steps.  This prevents a single training
// artifact or OOD spike from causing a large session-affinity-breaking update.
//
// Returns true when the caller should apply the candidate weights.
func (a *Agent) shouldApplyWeights(candidate []float64, nBackends int) bool {
	const (
		maxSingleStepDeltaPct = 0.10 // 10%
		vnodeBase             = 150
		requiredAgreements    = 2
	)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.prevAppliedW == nil || len(a.prevAppliedW) != len(candidate) {
		// First application — no gate, apply immediately.
		return true
	}

	// Compute total vnode counts for old and new weights.
	oldTotal, newTotal := 0, 0
	for i := range candidate {
		oldTotal += vnodeCount(a.prevAppliedW[i], vnodeBase, nBackends)
		newTotal += vnodeCount(candidate[i], vnodeBase, nBackends)
	}
	if oldTotal == 0 {
		return true
	}
	delta := math.Abs(float64(newTotal-oldTotal)) / float64(oldTotal)

	if delta <= maxSingleStepDeltaPct {
		// Small change — apply immediately, reset any pending state.
		a.pendingW = nil
		a.pendingAgreements = 0
		return true
	}

	// Large change detected.  Check if this step agrees with a pending candidate.
	if a.pendingW != nil && l2dist(a.pendingW, candidate) < 0.02 {
		a.pendingAgreements++
		if a.pendingAgreements >= requiredAgreements {
			// Two consecutive steps agree — safe to apply.
			a.pendingW = nil
			a.pendingAgreements = 0
			a.log.Info("oscillation gate: two-step consensus reached, applying large ring update",
				zap.Float64("vnode_delta_pct", delta*100),
			)
			return true
		}
	} else {
		// New direction or first large step — hold and record.
		a.pendingW = make([]float64, len(candidate))
		copy(a.pendingW, candidate)
		a.pendingAgreements = 1
	}
	a.log.Debug("oscillation gate: large ring update held pending second confirmation",
		zap.Float64("vnode_delta_pct", delta*100),
		zap.Int("agreements", a.pendingAgreements),
	)
	return false
}

func vnodeCount(weight float64, base, nBackends int) int {
	count := int(math.Round(weight * float64(base) * float64(nBackends)))
	if count < 1 {
		count = 1
	}
	return count
}

// collectState assembles the MDP state vector from the live metrics.GlobalCollector.
//
// State vector layout per backend (8 features × N backends + 4 global):
//
//	[0] latency_ewma_ms      — EWMA latency in milliseconds
//	[1] error_rate_1m        — exponential smoothed 1-min error rate (0-1)
//	[2] request_count_norm   — requests normalised by global max
//	[3..6] load_hist_norm    — last 4 load history samples (normalised)
//	[7] vnode_count_norm     — current vnode count / maxVnodes
//
// Global features (appended after all backend features):
//
//	[N*8+0] ring_balance_factor
//	[N*8+1] active_backends_count
//	[N*8+2] p99_latency_ms (global)
//	[N*8+3] global_error_rate
func collectState() ([]float64, []uint32) {
	if metrics.GlobalCollector == nil {
		return []float64{}, []uint32{}
	}
	snap := metrics.GlobalCollector.Snapshot()
	if len(snap) == 0 {
		return []float64{}, []uint32{}
	}

	// Stable ordering: sort backend IDs
	ids := make([]uint32, 0, len(snap))
	for id := range snap {
		ids = append(ids, id)
	}
	// Simple sort for stable state vector
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}

	// Find max request count for normalisation
	var maxReq uint64 = 1
	var globalErrSum, globalLatSum float64
	for _, s := range snap {
		s.RLock()
		if s.RequestCount > maxReq {
			maxReq = s.RequestCount
		}
		globalErrSum += s.ErrorRate1m
		globalLatSum += s.LatencyEWMA
		s.RUnlock()
	}

	const featuresPerBackend = 8
	state := make([]float64, 0, len(ids)*featuresPerBackend+4)

	for _, id := range ids {
		s := snap[id]
		s.RLock()
		latMs := s.LatencyEWMA
		errRate := s.ErrorRate1m
		reqNorm := float64(s.RequestCount) / float64(maxReq)
		hist := make([]float64, len(s.LoadHistory))
		copy(hist, s.LoadHistory)
		s.RUnlock()

		// Last 4 load history samples (zero-padded if shorter)
		var h4 [4]float64
		for k := 0; k < 4 && k < len(hist); k++ {
			idx := len(hist) - 4 + k
			if idx >= 0 {
				h4[k] = hist[idx] / 1000.0 // normalise ms→s scale
			}
		}
		state = append(state,
			latMs/1000.0,               // [0] latency_ewma_ms → norm
			errRate,                    // [1] error_rate_1m
			reqNorm,                    // [2] request_count_norm
			h4[0], h4[1], h4[2], h4[3], // [3..6] load history
			0.0, // [7] vnode_count_norm — filled by ring in future
		)
	}

	// Global features
	n := float64(len(ids))
	state = append(state,
		0.0,                   // [N*8+0] ring_balance_factor (from ring.Manager)
		n/64.0,                // [N*8+1] active_backends_count (norm by max 64)
		globalLatSum/n/1000.0, // [N*8+2] p99_latency approx (avg)
		globalErrSum/n,        // [N*8+3] global_error_rate
	)
	return state, ids
}

func uniformWeights(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 1.0 / float64(n)
	}
	return w
}

// applyWeightsToRing translates weight vector → vnode counts.
// More weight → more virtual nodes → more traffic.
func applyWeightsToRing(rm *ring.Manager, backends []uint32, weights []float64) {
	const totalVnodes = 150 // base per-server, scaled by weight
	for i, id := range backends {
		count := int(math.Round(weights[i] * float64(totalVnodes) * float64(len(backends))))
		if count < 1 {
			count = 1
		}
		rm.SetVnodeCount(id, count)
	}
}
