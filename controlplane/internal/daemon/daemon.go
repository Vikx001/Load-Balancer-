// Package daemon wires all Omega-LB subsystems and runs the main control loop.
package daemon

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/health"
	"github.com/omega-lb/omega-lb/internal/metrics"
	"github.com/omega-lb/omega-lb/internal/rl"
	"github.com/omega-lb/omega-lb/internal/ring"
	"github.com/omega-lb/omega-lb/internal/ratelimit"
	"github.com/omega-lb/omega-lb/internal/telemetry"
	"github.com/omega-lb/omega-lb/internal/xds"
)

// Daemon is the top-level orchestrator.
type Daemon struct {
	cfg     *config.Config
	log     *zap.Logger
	ring    *ring.Manager
	rl      *rl.Agent
	rl_dqn  *ratelimit.DQNAgent
	health  *health.Checker
	metrics *metrics.Collector
	telem   *telemetry.Exporter
	xds     *xds.Server
}

// New constructs and wires all subsystems.
func New(cfg *config.Config, log *zap.Logger) (*Daemon, error) {
	// ── Layer 1: Hash & Adjust ring ──────────────────────────────────────
	rm, err := ring.NewManager(cfg.Ring, log)
	if err != nil {
		return nil, fmt.Errorf("ring manager: %w", err)
	}

	// ── Layer 2 & 3: PPO + KAN actor + CBF projector ─────────────────────
	var agent *rl.Agent
	if cfg.RL.Enabled {
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
	}

	// ── Layer 4: DQN+A3C rate limiter ────────────────────────────────────
	var dqn *ratelimit.DQNAgent
	if cfg.RateLimit.Enabled {
		dqn, err = ratelimit.NewDQNAgent(cfg.RateLimit, log)
		if err != nil {
			return nil, fmt.Errorf("DQN rate limit agent: %w", err)
		}
	}

	// ── Health checker ───────────────────────────────────────────────────
	hc := health.NewChecker(cfg.Health, rm, log)

	// ── Metrics collector (reads eBPF ringbuf) ───────────────────────────
	mc, err := metrics.NewCollector(cfg.EBPF.PinPath, log)
	if err != nil {
		return nil, fmt.Errorf("metrics collector: %w", err)
	}

	// ── Telemetry exporter (OTLP) ────────────────────────────────────────
	te, err := telemetry.NewExporter(cfg.Telemetry, log)
	if err != nil {
		return nil, fmt.Errorf("telemetry exporter: %w", err)
	}

	// ── xDS gRPC server ──────────────────────────────────────────────────
	xs, err := xds.NewServer(cfg.XDS, rm, log)
	if err != nil {
		return nil, fmt.Errorf("xDS server: %w", err)
	}

	return &Daemon{
		cfg:     cfg,
		log:     log,
		ring:    rm,
		rl:      agent,
		rl_dqn:  dqn,
		health:  hc,
		metrics: mc,
		telem:   te,
		xds:     xs,
	}, nil
}

// Run starts all subsystems and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	// xDS gRPC server
	g.Go(func() error { return d.xds.Serve(ctx) })

	// Health checker
	g.Go(func() error { return d.health.Run(ctx) })

	// Metrics collector (eBPF ringbuf reader)
	g.Go(func() error { return d.metrics.Run(ctx) })

	// Telemetry exporter
	g.Go(func() error { return d.telem.Run(ctx) })

	// RL control loop (if enabled)
	if d.rl != nil {
		g.Go(func() error { return d.rl.Run(ctx) })
	}

	// Rate-limit control loop (if enabled)
	if d.rl_dqn != nil {
		g.Go(func() error { return d.rl_dqn.Run(ctx) })
	}

	// Proactive pre-distribution loop
	g.Go(func() error { return d.runProactiveLoop(ctx) })

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
