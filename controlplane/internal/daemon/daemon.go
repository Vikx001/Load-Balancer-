// Package daemon wires all Omega-LB subsystems and runs the main control loop.
package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/omega-lb/omega-lb/internal/admin"
	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/consensus"
	"github.com/omega-lb/omega-lb/internal/health"
	"github.com/omega-lb/omega-lb/internal/metrics"
	"github.com/omega-lb/omega-lb/internal/observability"
	"github.com/omega-lb/omega-lb/internal/ratelimit"
	"github.com/omega-lb/omega-lb/internal/ring"
	"github.com/omega-lb/omega-lb/internal/rl"
	"github.com/omega-lb/omega-lb/internal/telemetry"
	"github.com/omega-lb/omega-lb/internal/xds"
)

// Daemon is the top-level orchestrator.
type Daemon struct {
	cfg       *config.Config
	log       *zap.Logger
	stage     int
	ring      *ring.Manager
	rl        *rl.Agent
	rl_dqn    *ratelimit.DQNAgent
	health    *health.Checker
	metrics   *metrics.Collector
	telem     *telemetry.Exporter
	xds       *xds.Server
	cb        *health.CircuitBreakerManager
	pm        *ring.PoolMonitor
	admin     *admin.Server
	recorder  *observability.FlightRecorder
	consensus *consensus.Coordinator
}

// New constructs and wires all subsystems.
func New(cfg *config.Config, log *zap.Logger) (*Daemon, error) {
	stage := cfg.Stage
	if stage < 1 {
		stage = 1
	}
	if stage > 5 {
		stage = 5
	}

	// ── Stage announcement ───────────────────────────────────────────────
	// Each stage is a production-grade milestone.  Never advance without
	// benchmarking the current stage for at least 2 weeks in production.
	stageNames := map[int]string{
		1: "eBPF data plane + static round-robin",
		2: "H&A consistent hash ring",
		3: "health checker + metrics + circuit breaker",
		4: "RL shadow mode (observe only, does not affect routing)",
		5: "RL live traffic control",
	}
	log.Info("Omega-LB stage configuration",
		zap.Int("stage", stage),
		zap.String("stage_name", stageNames[stage]),
		zap.String("advice", "do not advance stages without 2+ weeks of production metrics"),
	)

	// ── Stage 1+: Hash & Adjust ring ────────────────────────────────────
	// Stage 1 uses round-robin (VnodesPerServer=0 → equal weights).
	// Stage 2+ enables H&A self-adjustment with full vnode counts.
	ringCfg := cfg.Ring
	if stage < 2 {
		// Static equal-weight distribution: all backends get 1 vnode.
		// The ring still works; it just doesn't self-adjust.
		ringCfg.VnodesPerServer = 1
		ringCfg.AdjustEveryN = 0 // disable H&A vnode adjustment
		log.Info("stage 1: H&A vnode adjustment disabled; using static equal-weight ring")
	}
	rm, err := ring.NewManager(ringCfg, log)
	if err != nil {
		return nil, fmt.Errorf("ring manager: %w", err)
	}

	// ── Stage 4+: PPO + KAN actor + CBF projector ─────────────────────
	// Stage 4 = shadow mode: agent runs, but SetMode(ModeAssisted) so it
	// observes without changing ring weights.
	// Stage 5 = live: SetMode(ModeAuto) — RL takes weight control.
	var agent *rl.Agent
	if stage >= 4 && cfg.RL.Enabled {
		kan, kanErr := rl.NewKANActor(cfg.RL.ONNXModelPath, log)
		if kanErr != nil {
			log.Warn("KAN actor unavailable, falling back to ring-only", zap.Error(kanErr))
			kan = nil
		}
		cbf, cbfErr := rl.NewCBFProjector(cfg.RL.CBFLambda, cfg.RL.CapacityPctCap, log)
		if cbfErr != nil {
			return nil, fmt.Errorf("CBF projector: %w", cbfErr)
		}
		var agentErr error
		agent, agentErr = rl.NewAgent(cfg.RL, kan, cbf, rm, log)
		if agentErr != nil {
			return nil, fmt.Errorf("RL agent: %w", agentErr)
		}
		if stage == 4 {
			// Shadow mode: agent observes but does not modify ring weights.
			agent.SetMode(rl.ModeAssisted, nil)
			log.Info("stage 4: RL agent in shadow mode (ModeAssisted); not routing traffic")
		} else {
			agent.SetMode(rl.ModeAuto, nil)
			log.Info("stage 5: RL agent live (ModeAuto); controlling ring weights")
		}
	}

	// ── Stage 5: DQN+A3C rate limiter ────────────────────────────────────
	var dqn *ratelimit.DQNAgent
	if stage >= 5 && cfg.RateLimit.Enabled {
		dqn, err = ratelimit.NewDQNAgent(cfg.RateLimit, log)
		if err != nil {
			return nil, fmt.Errorf("DQN rate limit agent: %w", err)
		}
	}

	// ── Stage 3+: Health checker ─────────────────────────────────────────
	var hc *health.Checker
	if stage >= 3 {
		hc = health.NewChecker(cfg.Health, rm, log)
	}

	// ── Stage 3+: Circuit breaker (reads circuit_state_map from eBPF) ────
	var cb *health.CircuitBreakerManager
	if stage >= 3 {
		cb = health.NewCircuitBreakerManager(cfg.EBPF.PinPath, log, rm.BeginSlowStart)
		if hc != nil {
			hc.SetRecoveryCallback(rm.BeginSlowStart)
		}
	}

	// ── Stage 3+: Metrics collector (reads eBPF ringbuf) ─────────────────
	var mc *metrics.Collector
	var fr *observability.FlightRecorder
	if stage >= 3 {
		mc, err = metrics.NewCollector(cfg.EBPF.PinPath, log)
		if err != nil {
			return nil, fmt.Errorf("metrics collector: %w", err)
		}
		fr = observability.NewFlightRecorder(cfg.Admin.FlightRecorderCapacity, log)
		mc.SetSampleHook(fr.RecordHook())
	}

	// ── Stage 3+: Telemetry exporter (OTLP) with cardinality guard ───────
	var te *telemetry.Exporter
	if stage >= 3 {
		te, err = telemetry.NewExporter(cfg.Telemetry, log)
		if err != nil {
			return nil, fmt.Errorf("telemetry exporter: %w", err)
		}
		budget := metrics.NewCardinalityBudget(cfg.Metrics.MaxLabelValuesPerDimension, log)
		te.SetCardinalityBudget(budget)
	}

	// ── Stage 3+: Pool monitor (i-sock pool drift detector) ───────────────
	var pm *ring.PoolMonitor
	if stage >= 3 {
		pm = ring.NewPoolMonitor(cfg.EBPF.PinPath, rm, log)
	}

	// ── Stage 2+: xDS gRPC server ─────────────────────────────────────────
	var xs *xds.Server
	if stage >= 2 {
		xs, err = xds.NewServer(cfg.XDS, rm, log)
		if err != nil {
			return nil, fmt.Errorf("xDS server: %w", err)
		}
	}

	// ── Stage 3+: Admin HTTP API (explain + mode switch) ─────────────────
	var adminSrv *admin.Server
	if stage >= 3 {
		adminSrv = admin.NewServer(cfg.Admin.ListenAddr, fr, agent, rm, nil, log)
		// consensus is wired below; the pointer will be set before Run() is called
	}

	// ── Stage 3+: Consensus coordinator (ring state sync) ─────────────────
	// Single-node: MemStateStore (always leader, in-memory).
	// Multi-node:  configure consensus.etcd_endpoints and add
	//              go.etcd.io/etcd/client/v3 to go.mod, then swap in EtcdStore.
	var coord *consensus.Coordinator
	if stage >= 3 {
		nodeID := cfg.Consensus.NodeID
		if nodeID == "" {
			if hostname, herr := os.Hostname(); herr == nil {
				nodeID = hostname
			} else {
				nodeID = "omega-lb-single-node"
			}
		}
		leaderKey := cfg.Consensus.LeaderKey
		if leaderKey == "" {
			leaderKey = "/omega-lb/leader"
		}
		stateKey := cfg.Consensus.RingStateKey
		if stateKey == "" {
			stateKey = "/omega-lb/ring-state"
		}
		ttl := cfg.Consensus.LockTTLSeconds
		if ttl <= 0 {
			ttl = 10
		}
		var store consensus.StateStore = consensus.NewMemStateStore()
		if len(cfg.Consensus.EtcdEndpoints) > 0 {
			log.Warn("etcd endpoints configured; using in-memory store (add go.etcd.io/etcd/client/v3 to go.mod for real etcd)",
				zap.Strings("etcd_endpoints", cfg.Consensus.EtcdEndpoints),
			)
		}
		coord = consensus.NewCoordinator(store, nodeID, leaderKey, stateKey, ttl, rm, log)
		// back-fill coordinator into admin server so /admin/consensus works
		if adminSrv != nil {
			adminSrv = admin.NewServer(cfg.Admin.ListenAddr, fr, agent, rm, coord, log)
		}
	}

	return &Daemon{
		cfg:       cfg,
		log:       log,
		stage:     stage,
		ring:      rm,
		rl:        agent,
		rl_dqn:    dqn,
		health:    hc,
		metrics:   mc,
		telem:     te,
		xds:       xs,
		cb:        cb,
		pm:        pm,
		admin:     adminSrv,
		recorder:  fr,
		consensus: coord,
	}, nil
}

// Run starts all subsystems and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	// Stage 2+: xDS gRPC server
	if d.xds != nil {
		g.Go(func() error { return d.xds.Serve(ctx) })
	}

	// Stage 3+: Health checker
	if d.health != nil {
		g.Go(func() error { return d.health.Run(ctx) })
	}

	// Stage 3+: Metrics collector (eBPF ringbuf reader) + flight recorder
	if d.metrics != nil {
		g.Go(func() error { return d.metrics.Run(ctx) })
	}

	// Stage 3+: Telemetry exporter
	if d.telem != nil {
		g.Go(func() error { return d.telem.Run(ctx) })
	}

	// Stage 3+: Circuit breaker state machine
	if d.cb != nil {
		g.Go(func() error { return d.cb.Run(ctx) })
	}

	// Stage 3+: i-sock pool drift monitor
	if d.pm != nil {
		g.Go(func() error { return d.pm.Run(ctx) })
	}

	// Stage 3+: Slow-start controller (H&A ring recovery after circuit opens)
	if d.stage >= 3 {
		g.Go(func() error { return d.ring.RunSlowStart(ctx) })
	}

	// Stage 3+: Admin HTTP API (explain + mode)
	if d.admin != nil {
		g.Go(func() error { return d.admin.Run(ctx) })
	}

	// Stage 3+: Consensus coordinator (ring state sync across nodes)
	if d.consensus != nil {
		g.Go(func() error { return d.consensus.Run(ctx) })
	}

	// Stage 4+: RL control loop (shadow or live, set in New())
	if d.rl != nil {
		g.Go(func() error { return d.rl.Run(ctx) })
	}

	// Stage 5: Rate-limit control loop
	if d.rl_dqn != nil {
		g.Go(func() error { return d.rl_dqn.Run(ctx) })
	}

	// Stage 2+: Proactive pre-distribution loop (requires metrics for prediction)
	if d.stage >= 2 && d.metrics != nil {
		g.Go(func() error { return d.runProactiveLoop(ctx) })
	}

	return g.Wait()
}

// runProactiveLoop: Layer 5 — predict and pre-migrate before saturation.
func (d *Daemon) runProactiveLoop(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.ring.ProactiveAdjust(d.metrics.LoadWindow())
		}
	}
}
