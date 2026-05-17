// Package metrics collects per-backend stats from the eBPF ringbuf and exposes
// them to the RL agent and telemetry exporter.
package metrics

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	loadWindowSize = 120 // 60s at 500ms sample rate
)

// Sample is a single metric observation from the eBPF ringbuf.
type Sample struct {
	InstanceID uint32
	LatencyNs  uint64
	Error      bool
	Timestamp  time.Time
}

// BackendStats aggregates recent samples for a backend.
type BackendStats struct {
	mu           sync.Mutex
	LatencyEWMA  float64
	ErrorRate1m  float64
	RequestCount uint64
	LoadHistory  []float64 // sliding window (loadWindowSize entries)
}

// Collector reads from the eBPF ringbuf and maintains per-backend stats.
type Collector struct {
	log      *zap.Logger
	pinPath  string
	backends sync.Map // uint32 → *BackendStats
}

// GlobalCollector is set by daemon wiring so rl.Agent can read state.
var GlobalCollector *Collector

// NewCollector creates the metrics collector.
func NewCollector(pinPath string, log *zap.Logger) (*Collector, error) {
	c := &Collector{log: log, pinPath: pinPath}
	GlobalCollector = c
	return c, nil
}

// Run reads events from the eBPF ringbuf and updates in-memory stats.
func (c *Collector) Run(ctx context.Context) error {
	// Real impl: open the events_ringbuf BPF map from pinPath,
	// use github.com/cilium/ebpf/ringbuf.Reader to consume samples.
	// Simplified: stub loop.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// In production: drain ringbuf events and call c.ingest(sample)
		}
	}
}

// Ingest processes a sample from the eBPF ringbuf.
func (c *Collector) Ingest(s Sample) {
	statsVal, _ := c.backends.LoadOrStore(s.InstanceID, &BackendStats{})
	stats := statsVal.(*BackendStats)

	stats.mu.Lock()
	defer stats.mu.Unlock()

	stats.RequestCount++

	// EWMA latency (α=0.125)
	lat := float64(s.LatencyNs) / 1e6 // → ms
	stats.LatencyEWMA = (stats.LatencyEWMA*7 + lat) / 8

	// Error rate: simple exponential smoothing
	errVal := 0.0
	if s.Error {
		errVal = 1.0
	}
	stats.ErrorRate1m = (stats.ErrorRate1m*59 + errVal) / 60

	// Append to load history (ring buffer)
	stats.LoadHistory = append(stats.LoadHistory, stats.LatencyEWMA)
	if len(stats.LoadHistory) > loadWindowSize {
		stats.LoadHistory = stats.LoadHistory[1:]
	}
}

// Snapshot returns a copy of the current stats for the RL state vector.
func (c *Collector) Snapshot() map[uint32]*BackendStats {
	result := make(map[uint32]*BackendStats)
	c.backends.Range(func(k, v any) bool {
		result[k.(uint32)] = v.(*BackendStats)
		return true
	})
	return result
}

// LoadWindow returns per-backend load history for proactive pre-distribution.
func (c *Collector) LoadWindow() map[uint32][]float64 {
	result := make(map[uint32][]float64)
	c.backends.Range(func(k, v any) bool {
		stats := v.(*BackendStats)
		stats.mu.Lock()
		hist := make([]float64, len(stats.LoadHistory))
		copy(hist, stats.LoadHistory)
		stats.mu.Unlock()
		result[k.(uint32)] = hist
		return true
	})
	return result
}
