// Package ratelimit implements Layer 4: DQN + A3C adaptive rate limiting.
// Reference: arXiv 2511.03279 — Multi-Objective Adaptive Rate Limiting.
package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
)

// Action codes for DQN
const (
	ActionDecrease = 0 // decrease limit by 10%
	ActionHold     = 1
	ActionIncrease = 2 // increase limit by 10%
)

// State observed per service
type serviceState struct {
	currentRPS    float64
	backendCPUPct float64
	queueDepth    float64
	errorRate5m   float64
	p99LatencyMs  float64
	currentLimit  float64
}

// ServiceLimiter tracks and adjusts the rate limit for one service.
type ServiceLimiter struct {
	mu           sync.Mutex
	serviceID    uint32
	currentLimit float64
	minRPS       float64
	maxRPS       float64
	replayBuffer []transition
	qNetwork     *dqnNetwork
	log          *zap.Logger
}

type transition struct {
	state   serviceState
	action  int
	reward  float64
	next    serviceState
}

// DQNAgent orchestrates all per-service limiters + A3C global actor.
type DQNAgent struct {
	cfg      config.RateLimitConfig
	log      *zap.Logger
	services map[uint32]*ServiceLimiter
	a3c      *a3cGlobalActor
}

// NewDQNAgent creates the DQN+A3C rate limiting agent.
func NewDQNAgent(cfg config.RateLimitConfig, log *zap.Logger) (*DQNAgent, error) {
	services := make(map[uint32]*ServiceLimiter)
	for _, svc := range cfg.Services {
		net := newDQNNetwork(6, 3) // state_dim=6, action_dim=3
		services[svc.ServiceID] = &ServiceLimiter{
			serviceID:    svc.ServiceID,
			currentLimit: svc.InitialRPS,
			minRPS:       svc.MinRPS,
			maxRPS:       svc.MaxRPS,
			qNetwork:     net,
			log:          log,
		}
	}
	return &DQNAgent{
		cfg:      cfg,
		log:      log,
		services: services,
		a3c:      newA3CGlobalActor(log),
	}, nil
}

// Run is the main DQN+A3C update loop (every 100ms per spec).
func (d *DQNAgent) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(d.cfg.UpdateIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.step()
		}
	}
}

func (d *DQNAgent) step() {
	for id, svc := range d.services {
		st := d.observeState(id)
		action := svc.selectAction(st)
		reward := computeReward(st, action)
		svc.applyAction(action)
		d.log.Debug("rate limiter step",
			zap.Uint32("service", id),
			zap.Int("action", action),
			zap.Float64("limit", svc.currentLimit),
			zap.Float64("reward", reward),
		)
		// Store transition for experience replay
		svc.mu.Lock()
		if len(svc.replayBuffer) < 50000 {
			svc.replayBuffer = append(svc.replayBuffer, transition{
				state:  st,
				action: action,
				reward: reward,
				next:   d.observeState(id),
			})
		}
		svc.mu.Unlock()
		// A3C global actor update
		d.a3c.contribute(id, st, action, reward)
	}
	d.a3c.globalUpdate()
}

// observeState collects the MDP state for a service (from metrics).
func (d *DQNAgent) observeState(serviceID uint32) serviceState {
	// Real impl: query metrics.GlobalCollector.ServiceState(serviceID)
	return serviceState{
		currentRPS:   100,
		backendCPUPct: 0.5,
		currentLimit: d.services[serviceID].currentLimit,
	}
}

func computeReward(s serviceState, action int) float64 {
	reward := s.currentRPS                     // throughput
	reward -= s.errorRate5m * 100             // penalise 5xx
	reward -= math.Max(0, s.p99LatencyMs-200) // penalise latency > 200ms
	if s.backendCPUPct > 0.90 {
		return -1e9 // backend overloaded — hard penalty
	}
	return reward
}

// selectAction uses ε-greedy DQN policy.
func (s *ServiceLimiter) selectAction(st serviceState) int {
	const epsilon = 0.1
	if mathRandFloat() < epsilon {
		return int(mathRandIntn(3))
	}
	stateVec := stateToVec(st)
	qvals := s.qNetwork.forward(stateVec)
	return argmax(qvals)
}

func (s *ServiceLimiter) applyAction(action int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case ActionDecrease:
		s.currentLimit = math.Max(s.minRPS, s.currentLimit*0.90)
	case ActionIncrease:
		s.currentLimit = math.Min(s.maxRPS, s.currentLimit*1.10)
	}
	// Real impl: write s.currentLimit to eBPF rate_limit_map[serviceID]
}

// CurrentLimit returns the current rate limit for a service (read by eBPF sync).
func (d *DQNAgent) CurrentLimit(serviceID uint32) float64 {
	if svc, ok := d.services[serviceID]; ok {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.currentLimit
	}
	return -1
}

// ─── Minimal DQN network (linear Q-network for demonstration) ─────────────
// In production this is replaced by the ONNX-compiled PyTorch DQN.

type dqnNetwork struct {
	stateDim  int
	actionDim int
	// w: [actionDim][stateDim] weight matrix (single-layer linear)
	w [][]float64
	b []float64
}

func newDQNNetwork(stateDim, actionDim int) *dqnNetwork {
	w := make([][]float64, actionDim)
	b := make([]float64, actionDim)
	for i := range w {
		w[i] = make([]float64, stateDim)
		// Xavier init
		scale := math.Sqrt(2.0 / float64(stateDim))
		for j := range w[i] {
			w[i][j] = (mathRandFloat()*2 - 1) * scale
		}
	}
	return &dqnNetwork{stateDim: stateDim, actionDim: actionDim, w: w, b: b}
}

func (n *dqnNetwork) forward(state []float64) []float64 {
	q := make([]float64, n.actionDim)
	for a := 0; a < n.actionDim; a++ {
		for s := 0; s < n.stateDim && s < len(state); s++ {
			q[a] += n.w[a][s] * state[s]
		}
		q[a] += n.b[a]
	}
	return q
}

// ─── A3C global actor ─────────────────────────────────────────────────────

type a3cContrib struct {
	serviceID uint32
	state     serviceState
	action    int
	reward    float64
}

type a3cGlobalActor struct {
	mu      sync.Mutex
	log     *zap.Logger
	contribs []a3cContrib
}

func newA3CGlobalActor(log *zap.Logger) *a3cGlobalActor {
	return &a3cGlobalActor{log: log}
}

func (a *a3cGlobalActor) contribute(serviceID uint32, st serviceState, action int, reward float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contribs = append(a.contribs, a3cContrib{serviceID, st, action, reward})
}

// globalUpdate applies a gradient step using aggregated experience.
// Simplified: log aggregate reward for now; real impl updates shared weights.
func (a *a3cGlobalActor) globalUpdate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.contribs) == 0 {
		return
	}
	var totalReward float64
	for _, c := range a.contribs {
		totalReward += c.reward
	}
	a.log.Debug("A3C global update",
		zap.Int("contribs", len(a.contribs)),
		zap.Float64("mean_reward", totalReward/float64(len(a.contribs))),
	)
	a.contribs = a.contribs[:0]
}

// ─── Utility ──────────────────────────────────────────────────────────────

func stateToVec(s serviceState) []float64 {
	return []float64{
		s.currentRPS / 10000,
		s.backendCPUPct,
		s.queueDepth / 1000,
		s.errorRate5m,
		s.p99LatencyMs / 1000,
		s.currentLimit / 10000,
	}
}

func argmax(v []float64) int {
	best := 0
	for i := 1; i < len(v); i++ {
		if v[i] > v[best] {
			best = i
		}
	}
	return best
}

// mathRandFloat / mathRandIntn are thin wrappers so we can swap in crypto/rand in tests.
var (
	_rngSeed uint64 = 0xdeadbeef
)

func mathRandFloat() float64 {
	_rngSeed ^= _rngSeed << 13
	_rngSeed ^= _rngSeed >> 7
	_rngSeed ^= _rngSeed << 17
	return float64(_rngSeed&0x7FFFFFFFFFFFFFFF) / float64(0x7FFFFFFFFFFFFFFF)
}

func mathRandIntn(n int) int {
	return int(mathRandFloat() * float64(n))
}
