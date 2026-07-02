package rl

import (
	"math"
	"sync"

	"go.uber.org/zap"
)

// OODDetector detects out-of-distribution (OOD) state vectors using an online
// z-score test against the running training distribution.
//
// ─── FAILURE MODE: state space distribution shift ─────────────────────────────
// The PPO model learns a policy over the state distribution seen during training.
// Traffic patterns that were not present during training (flash crowds, DDoS
// patterns, clients with unusually heavy connections, Black Friday spikes) will
// produce state vectors that lie outside the region where the value function is
// well-fitted.  The model still produces outputs in those states, but those
// outputs are extrapolations and may be systematically wrong.
//
// Detection approach:
//
//	For each incoming state vector s, compute the per-dimension z-score:
//	  z_i = |s_i − μ_i| / (σ_i + ε)
//	The OOD score is max(z_i) across all dimensions.
//	If OOD score > threshold (default: 3σ), the state is flagged as OOD.
//
// This is simpler than Mahalanobis distance (which requires a full covariance
// matrix) and catches the single-feature deviations that are most common in
// load-balancer traffic anomalies.
//
// When a state is OOD, the caller (rl.Agent) reduces the model's influence in
// the action-smoothing formula, falling back toward the H&A ring distribution.
// OOD events are logged so operators can identify when retraining is needed.
type OODDetector struct {
	mu        sync.Mutex
	log       *zap.Logger
	threshold float64 // z-score threshold; default 3.0

	// Welford's online algorithm for running mean and variance.
	n    int64
	mean []float64
	m2   []float64 // sum of squared deviations
	dim  int
}

// NewOODDetector creates a new OOD detector.
// threshold is the z-score limit above which a state is considered OOD (default 3.0).
func NewOODDetector(dim int, threshold float64, log *zap.Logger) *OODDetector {
	if threshold <= 0 {
		threshold = 3.0
	}
	return &OODDetector{
		log:       log,
		threshold: threshold,
		dim:       dim,
		mean:      make([]float64, dim),
		m2:        make([]float64, dim),
	}
}

// Update incorporates a new state vector into the running distribution estimate.
// Call this on every state vector that is considered in-distribution (i.e. during
// the warm-up period or after a shadow-mode validation confirms normality).
func (o *OODDetector) Update(state []float64) {
	if len(state) != o.dim {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	o.n++
	for i, x := range state {
		delta := x - o.mean[i]
		o.mean[i] += delta / float64(o.n)
		delta2 := x - o.mean[i]
		o.m2[i] += delta * delta2
	}
}

// Score returns the OOD score (max z-score across all dimensions) for state.
// A score > threshold indicates an OOD state.
// Returns 0.0 if fewer than 30 states have been observed (not enough history).
func (o *OODDetector) Score(state []float64) float64 {
	if len(state) != o.dim {
		return 0.0
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.n < 30 {
		return 0.0 // not enough history to make a reliable estimate
	}

	var maxZ float64
	for i, x := range state {
		variance := 0.0
		if o.n > 1 {
			variance = o.m2[i] / float64(o.n-1)
		}
		stddev := math.Sqrt(variance)
		if stddev < 1e-8 {
			continue // near-constant feature; skip
		}
		z := math.Abs(x-o.mean[i]) / stddev
		if z > maxZ {
			maxZ = z
		}
	}
	return maxZ
}

// IsOOD returns true if the state's z-score exceeds the threshold.
func (o *OODDetector) IsOOD(state []float64) bool {
	return o.Score(state) > o.threshold
}

// ActionWeight returns the model weight to use in the action-smoothing formula
// given the OOD score.  Returns a value in [0, 1] that linearly decreases from
// the nominal model weight toward 0 as the OOD score increases past the threshold.
//
//	score < threshold           → return nominalModelWeight (full model influence)
//	threshold ≤ score < 2×thr  → linearly interpolate toward 0
//	score ≥ 2×threshold         → return 0 (ring-only routing)
//
// The caller uses this as the α in: w = α·w_model + (1−α)·w_ring
func (o *OODDetector) ActionWeight(score, nominalModelWeight float64) float64 {
	if score <= o.threshold {
		return nominalModelWeight
	}
	if score >= 2*o.threshold {
		return 0.0
	}
	// Linear interpolation
	t := (score - o.threshold) / o.threshold // 0..1
	return nominalModelWeight * (1 - t)
}

// LogOODEvent logs an OOD detection event at Warning level.
// Repeated OOD events with similar scores suggest the training distribution
// has permanently shifted and retraining is required.
func (o *OODDetector) LogOODEvent(score float64, state []float64) {
	o.log.Warn("OOD state detected — model extrapolating outside training distribution",
		zap.Float64("ood_score_sigma", score),
		zap.Float64("threshold_sigma", o.threshold),
		zap.Int("state_dim", len(state)),
		zap.String("action", "model weight reduced in smoothing formula"),
		zap.String("recommendation",
			"if OOD events persist >1h, collect this traffic pattern and schedule retraining"),
	)
}

// SampleCount returns the number of state vectors observed so far.
func (o *OODDetector) SampleCount() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.n
}
