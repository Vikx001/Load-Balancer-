package telemetry

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/metrics"
	"github.com/omega-lb/omega-lb/internal/ring"
)

// findExportField returns the value of the named field on the "telemetry
// export" log entry for the given backend_id, or (nil, false) if no such
// entry/field exists.
func findExportField(logs []observer.LoggedEntry, backendID uint32, field string) (interface{}, bool) {
	for _, entry := range logs {
		ctx := entry.ContextMap()
		gotID, hasID := ctx["backend_id"].(uint32)
		if !hasID || gotID != backendID {
			continue
		}
		if v, ok := ctx[field]; ok {
			return v, true
		}
	}
	return nil, false
}

func TestExportReportsDrainingStateFromRingManager(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	log := zap.New(core)

	rm, err := ring.NewManager(config.RingConfig{AdjustEveryN: 100, AdjustThreshold: 1.30}, zap.NewNop())
	if err != nil {
		t.Fatalf("ring.NewManager: %v", err)
	}
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})
	rm.AddBackend(&ring.Backend{ID: 2, Health: true, CapacityMax: 1000})
	rm.SetDraining(2, true)

	collector, err := metrics.NewCollector("", zap.NewNop())
	if err != nil {
		t.Fatalf("metrics.NewCollector: %v", err)
	}
	// Simulate the eBPF ringbuf having observed traffic for both backends,
	// including the one that is now draining (its in-flight requests still
	// generate samples right up until they finish).
	collector.Ingest(metrics.Sample{InstanceID: 1, LatencyNs: 2_000_000, Timestamp: time.Now()})
	collector.Ingest(metrics.Sample{InstanceID: 2, LatencyNs: 3_000_000, Timestamp: time.Now()})

	exp, err := NewExporter(config.TelemetryConfig{ExportIntervalS: 1}, log)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	exp.SetRingManager(rm)

	exp.export()

	draining1, ok := findExportField(logs.All(), 1, "omega_lb_backend_draining")
	if !ok || draining1 != false {
		t.Fatalf("expected backend 1 draining=false, got %v (found=%v)", draining1, ok)
	}
	draining2, ok := findExportField(logs.All(), 2, "omega_lb_backend_draining")
	if !ok || draining2 != true {
		t.Fatalf("expected backend 2 draining=true, got %v (found=%v)", draining2, ok)
	}
	healthy2, ok := findExportField(logs.All(), 2, "omega_lb_backend_healthy")
	if !ok || healthy2 != true {
		t.Fatalf("draining must not imply unhealthy: expected backend 2 healthy=true, got %v (found=%v)", healthy2, ok)
	}
}

func TestExportWithoutRingManagerDoesNotPanic(t *testing.T) {
	core, _ := observer.New(zapcore.DebugLevel)
	log := zap.New(core)

	collector, err := metrics.NewCollector("", zap.NewNop())
	if err != nil {
		t.Fatalf("metrics.NewCollector: %v", err)
	}
	collector.Ingest(metrics.Sample{InstanceID: 1, LatencyNs: 1_000_000, Timestamp: time.Now()})

	exp, err := NewExporter(config.TelemetryConfig{ExportIntervalS: 1}, log)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	// Deliberately not calling SetRingManager — must degrade gracefully.
	exp.export()
}
