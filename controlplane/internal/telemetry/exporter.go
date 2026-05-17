// Package telemetry exports metrics via OpenTelemetry OTLP gRPC.
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
	cfg config.TelemetryConfig
	log *zap.Logger
}

// NewExporter constructs the telemetry exporter.
// Real impl: initialise otel SDK with otlpmetricgrpc exporter.
func NewExporter(cfg config.TelemetryConfig, log *zap.Logger) (*Exporter, error) {
	return &Exporter{cfg: cfg, log: log}, nil
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

	for id, stats := range snap {
		stats.RLock()
		lat := stats.LatencyEWMA
		errRate := stats.ErrorRate1m
		reqCount := stats.RequestCount
		stats.RUnlock()

		e.log.Debug("telemetry export",
			zap.Uint32("backend", id),
			zap.Float64("ewma_latency_ms", lat),
			zap.Float64("error_rate_1m", errRate),
			zap.Uint64("total_requests", reqCount),
		)
		// Real impl: emit these as OTLP metrics via otel.Meter().
		// Metric names: omega_lb.backend.latency_ms, omega_lb.backend.error_rate,
		//               omega_lb.backend.requests_total, omega_lb.ring.balance_factor,
		//               omega_lb.rl.action_weights, omega_lb.cbf.projection_magnitude
	}
}
