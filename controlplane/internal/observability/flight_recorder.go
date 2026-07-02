// Package observability provides the eBPF flight recorder and structured
// decision logging for Omega-LB.
//
// ─── THE PRODUCTION OBSERVABILITY PROBLEM ────────────────────────────────────
// When something goes wrong in production — a backend gets over-loaded, sessions
// drop, the RL agent makes a bad decision — engineers need to answer:
//
//	"At 03:47:12.391, why did the load balancer route request X to backend C?"
//
// This is impossible with:
//   - bpf_trace_printk(): rate-limited to 1/CPU/s; useless under load
//   - Prometheus counters: aggregated; lose per-request context
//   - grep on access logs: no knowledge of LB decision state at time of routing
//
// This package solves it with two mechanisms:
//
//  1. FlightRecorder: an in-memory ring buffer of the last ringbufCapacity
//     routing events.  Each event includes backend selected, latency EWMA at
//     selection, circuit state, probe index, and reason (normal/half-open/fallback).
//     Exposed via GET /admin/explain?backend_id=X or GET /admin/explain/recent.
//
//  2. Structured decision log: every event emits a structured zap log at DEBUG
//     level.  In production, ship these to your log aggregation stack (Loki,
//     Elasticsearch) and query them with the request_id field.
//
// Development workflow:
//
//	$ bpftool prog tracelog   # watch bpf_trace_printk output during dev
//	$ curl http://localhost:9000/admin/explain/recent | jq .
//	$ tail -f /var/log/omega-lb.log | grep '"msg":"routing_decision"'
package observability

import (
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/metrics"
)

const (
	// ringbufCapacity is the number of routing decisions retained in memory.
	// At 100k RPS, 10,000 events covers the last 100ms — enough for incident
	// analysis without unbounded memory growth.
	ringbufCapacity = 10_000
)

// RoutingDecision is a single recorded routing event from the eBPF flight recorder.
// This is the "why did you route here?" record for the /admin/explain API.
type RoutingDecision struct {
	// When the decision was made (from kernel bpf_ktime_get_ns, converted to wall clock)
	Timestamp time.Time `json:"timestamp"`

	// Backend that received this request
	BackendID uint32 `json:"backend_id"`

	// LatencyNs is the EWMA latency for this backend at selection time
	LatencyNs uint64 `json:"latency_ewma_ns"`

	// Error: true if the response was 5xx
	Error bool `json:"error"`

	// CircuitState at the time of selection (0=CLOSED, 1=OPEN, 2=HALF_OPEN)
	CircuitState uint32 `json:"circuit_state"`

	// VnodesAtSelect: how many virtual nodes this backend had when selected
	VnodesAtSelect uint32 `json:"vnodes_at_select"`

	// ProbeIdx: which clockwise probe in the 64-iteration loop found this backend.
	// 0 = ideal (hash lands exactly on this backend).
	// High ProbeIdx = many backends were skipped (overloaded, OPEN, etc.).
	ProbeIdx uint8 `json:"probe_idx"`

	// Reason: why this backend was selected
	// 0=normal H&A selection, 1=HALF_OPEN circuit probe, 2=last-resort fallback
	Reason uint8 `json:"reason"`

	// ReasonLabel is Reason decoded to a human-readable string
	ReasonLabel string `json:"reason_label"`
}

// FlightRecorder is a ring buffer of the last N routing decisions.
// It is safe for concurrent use.
type FlightRecorder struct {
	mu       sync.RWMutex
	buf      []RoutingDecision
	head     int // next write position (wraps)
	count    int // total writes (to distinguish empty from full)
	capacity int
	log      *zap.Logger
}

// NewFlightRecorder creates a FlightRecorder that retains the last capacity
// routing decisions.
func NewFlightRecorder(capacity int, log *zap.Logger) *FlightRecorder {
	if capacity <= 0 {
		capacity = ringbufCapacity
	}
	return &FlightRecorder{
		buf:      make([]RoutingDecision, capacity),
		capacity: capacity,
		log:      log,
	}
}

// RecordHook returns a function compatible with metrics.Collector.SetSampleHook.
// Wire it with:
//
//	mc.SetSampleHook(fr.RecordHook())
func (fr *FlightRecorder) RecordHook() func(metrics.Sample) {
	return func(s metrics.Sample) {
		fr.record(s)
	}
}

func (fr *FlightRecorder) record(s metrics.Sample) {
	reason := s.Reason
	reasonLabel := reasonLabel(reason)

	d := RoutingDecision{
		Timestamp:      s.Timestamp,
		BackendID:      s.InstanceID,
		LatencyNs:      s.LatencyNs,
		Error:          s.Error,
		CircuitState:   s.CircuitState,
		VnodesAtSelect: s.VnodesAtSelect,
		ProbeIdx:       s.ProbeIdx,
		Reason:         reason,
		ReasonLabel:    reasonLabel,
	}

	fr.mu.Lock()
	fr.buf[fr.head] = d
	fr.head = (fr.head + 1) % fr.capacity
	fr.count++
	fr.mu.Unlock()

	// Structured decision log — the "black box flight recorder" in log form.
	// In production: set log level to DEBUG; ship to Loki/Elastic with the
	// backend_id and request_id fields indexed for fast incident queries.
	fr.log.Debug("routing_decision",
		zap.Uint32("backend_id", d.BackendID),
		zap.Uint64("latency_ewma_ns", d.LatencyNs),
		zap.Bool("error", d.Error),
		zap.Uint32("circuit_state", d.CircuitState),
		zap.Uint32("vnodes_at_select", d.VnodesAtSelect),
		zap.Uint8("probe_idx", d.ProbeIdx),
		zap.String("reason", d.ReasonLabel),
		zap.Int64("ts_ns", d.Timestamp.UnixNano()),
	)

	// Emit at WARN when the fallback reason fires — this indicates all preferred
	// backends were unavailable and a last-resort backend was used.
	if reason == metrics.ReasonFallback {
		fr.log.Warn("routing_fallback: all preferred backends unavailable; using last resort",
			zap.Uint32("backend_id", d.BackendID),
			zap.Uint8("probe_idx", d.ProbeIdx),
			zap.String("action", "check circuit_state_map: bpftool map dump pinned <pin>/circuit_state_map"),
		)
	}
}

// Recent returns the last n decisions in chronological order (oldest first).
// If n ≤ 0 or n > capacity, all retained decisions are returned.
func (fr *FlightRecorder) Recent(n int) []RoutingDecision {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	size := fr.count
	if size > fr.capacity {
		size = fr.capacity
	}
	if n <= 0 || n > size {
		n = size
	}
	if n == 0 {
		return nil
	}

	out := make([]RoutingDecision, n)
	// Read backwards from head-1 (most recent), then reverse for chronological order.
	for i := 0; i < n; i++ {
		idx := ((fr.head - 1 - i) + fr.capacity*2) % fr.capacity
		out[n-1-i] = fr.buf[idx]
	}
	return out
}

// ForBackend returns recent decisions that selected the given backend.
// Returns at most maxResults decisions, newest first.
func (fr *FlightRecorder) ForBackend(backendID uint32, maxResults int) []RoutingDecision {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	size := fr.count
	if size > fr.capacity {
		size = fr.capacity
	}

	var out []RoutingDecision
	for i := 0; i < size && len(out) < maxResults; i++ {
		idx := ((fr.head - 1 - i) + fr.capacity*2) % fr.capacity
		d := fr.buf[idx]
		if d.BackendID == backendID {
			out = append(out, d)
		}
	}
	return out
}

func reasonLabel(r uint8) string {
	switch r {
	case metrics.ReasonNormal:
		return "normal"
	case metrics.ReasonHalfOpen:
		return "half_open_probe"
	case metrics.ReasonFallback:
		return "fallback"
	default:
		return "unknown"
	}
}
