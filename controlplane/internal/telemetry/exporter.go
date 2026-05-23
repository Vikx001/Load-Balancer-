// Package telemetry exports metrics via OpenTelemetry OTLP gRPC.
//
// ─── CARDINALITY GUARD ───────────────────────────────────────────────────────
// High-cardinality labels (path, user_id, backend_ip) must NEVER be emitted as
// Prometheus label dimensions.  Each unique label combination creates a separate
// time-series.  A single deployment with 200 paths × 20 backends × 50 status
// codes = 200,000 active series — enough to OOM a Prometheus node within hours.
//
// This exporter enforces the following label set for all per-request metrics:
//   ALLOWED:   backend_id (opaque uint32), service_id (opaque uint32)
//   DISALLOWED as labels: backend_ip, path, method, user_id, request_id
//
// Path-level analysis belongs in structured logs and exemplars, not counters.
package telemetry

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/metrics"
)

// Exporter periodically pushes metrics to an OTLP endpoint.
type Exporter struct {
	cfg    config.TelemetryConfig
	log    *zap.Logger
	budget *metrics.CardinalityBudget
}

// NewExporter constructs the telemetry exporter.
// Real impl: initialise otel SDK with otlpmetricgrpc exporter.
func NewExporter(cfg config.TelemetryConfig, log *zap.Logger) (*Exporter, error) {
	return &Exporter{cfg: cfg, log: log}, nil
}

// SetCardinalityBudget wires the cardinality guard.  Must be called before Run.
func (e *Exporter) SetCardinalityBudget(b *metrics.CardinalityBudget) {
	e.budget = b
}

// Run exports metrics on the configured interval.
func (e *Exporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(e.cfg.ExportIntervalS) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.export()
		}
	}
}

func (e *Exporter) export() {
	if metrics.GlobalCollector == nil {
		return
	}
	snap := metrics.GlobalCollector.Snapshot()

	// Emit cardinality overflow counter as its own metric so operators can
	// alert before the budget is silently swallowing new label values.
	var overflows uint64
	if e.budget != nil {
		overflows = e.budget.OverflowCount()
	}

	for id, stats := range snap {
		stats.RLock()
		lat := stats.LatencyEWMA
		errRate := stats.ErrorRate1m
		reqCount := stats.RequestCount
		stats.RUnlock()

		// Label normalization: backend_id is an opaque uint32.
		// DO NOT add backend_ip, path, or method here — they are high-cardinality.
		// If path-level breakdown is needed, use exemplars (OpenTelemetry traces).
		//
		// backend_id and service_id are the ONLY label dimensions on per-backend
		// metrics.  Everything else is aggregated or placed in structured logs.
		e.log.Debug("telemetry export",
			zap.Uint32("backend_id", id),
			zap.Float64("omega_lb_backend_latency_ms", lat),
			zap.Float64("omega_lb_backend_error_rate", errRate),
			zap.Uint64("omega_lb_backend_requests_total", reqCount),
			zap.Uint64("omega_lb_cardinality_overflows_total", overflows),
		)
		// Real impl: emit these as OTLP metrics via otel.Meter().
		// Metric names:
		//   omega_lb_backend_latency_ms{backend_id}         — EWMA latency
		//   omega_lb_backend_error_rate{backend_id}         — 1-min error rate
		//   omega_lb_backend_requests_total{backend_id}     — cumulative requests
		//   omega_lb_ring_balance_factor                    — ring-level only
		//   omega_lb_rl_action_weights{backend_id}          — RL weight per backend
		//   omega_lb_cbf_projection_magnitude               — CBF correction magnitude
		//   omega_lb_cardinality_overflows_total            — alert: label cap hit
		_ = overflows // used in real OTLP path
	}
}
