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
	"github.com/omega-lb/omega-lb/internal/ring"
)

// Agent is the main RL control loop: observe → infer → project → act.
type Agent struct {
	cfg      config.RLConfig
	log      *zap.Logger
	kan      *KANActor
	cbf      *CBFProjector
	ring     *ring.Manager
	prevW    []float64
	mu       sync.Mutex
}

// NewAgent constructs the RL agent.
func NewAgent(cfg config.RLConfig, kan *KANActor, cbf *CBFProjector,
	rm *ring.Manager, log *zap.Logger) (*Agent, error) {
	return &Agent{
		cfg:  cfg,
		log:  log,
		kan:  kan,
		cbf:  cbf,
		ring: rm,
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

	// 2. KAN actor inference (with timeout fallback)
	var rawW []float64
	var err error
	if a.kan != nil {
		ctx, cancel := context.WithTimeout(context.Background(),
			time.Duration(a.cfg.InferenceTimeoutMs)*time.Millisecond)
		rawW, err = a.kan.Infer(ctx, state)
		cancel()
		if err != nil {
			a.log.Warn("KAN inference failed, using uniform weights", zap.Error(err))
			rawW = uniformWeights(len(backends))
		}
	} else {
		rawW = uniformWeights(len(backends))
	}

	// 3. CBF safety projection: ensure no server exceeds capacity
	cbfBackends := make([]CBFBackendState, len(backends))
	for i, id := range backends {
		cbfBackends[i] = CBFBackendState{ID: id}
	}	safeW, err := a.cbf.Project(rawW, state, cbfBackends)
	if err != nil {
		a.log.Warn("CBF projection failed, using raw weights", zap.Error(err))
		safeW = rawW
	}

	// 4. Action smoothing: w_new = α×w_predicted + (1-α)×w_previous
	a.mu.Lock()
	if a.prevW == nil || len(a.prevW) != len(safeW) {
		a.prevW = safeW
	} else {
		alpha := a.cfg.ActionSmoothing
		for i := range safeW {
			safeW[i] = alpha*safeW[i] + (1-alpha)*a.prevW[i]
		}
		a.prevW = safeW
	}
	a.mu.Unlock()

	// 5. Apply weights to H&A ring (update vnode counts)
	applyWeightsToRing(a.ring, backends, safeW)

	a.log.Debug("RL step complete",
		zap.Float64s("weights", safeW),
		zap.Int("backends", len(backends)),
	)
	return nil
}

// collectState assembles the MDP state vector.
// In production this reads from the metrics.Collector; simplified here.
func collectState() ([]float64, []uint32) {
	// Returns (state_vector, backend_ids)
	// Real impl: query metrics.GlobalCollector.Snapshot()
	return []float64{}, []uint32{}
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
