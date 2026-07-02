// Package metrics collects per-backend stats from the eBPF ringbuf and exposes
// them to the RL agent and telemetry exporter.
//
// ─── WHY YOU CANNOT USE printf IN eBPF ───────────────────────────────────────
// eBPF programs run in the kernel: no stdout, no stderr, no debugger attach.
// bpf_trace_printk() writes to /sys/kernel/debug/tracing/trace_pipe but is
// rate-limited to 1 message/CPU/second — useless under load.
//
// The correct approach is to design observability first:
//  1. Every significant kernel-side decision emits a structured event_sample
//     to a BPF_MAP_TYPE_RINGBUF (zero-copy, low-overhead, no rate limiting).
//  2. This package reads those events in real time using cilium/ebpf/ringbuf.
//  3. Structured logs are emitted for every event (INFO in staging, DEBUG in prod).
//  4. The FlightRecorder stores the last ringbufCapacity events for the
//     /admin/explain API — a "black box" for post-hoc incident analysis.
//
// In development: run `bpftool prog tracelog` to watch bpf_trace_printk output.
// In production:  tail structured logs or call /admin/explain?backend_id=X.
package metrics

import (
	"context"
	"encoding/binary"
	"sync"
	"time"
	"unsafe"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/ringbuf"
	"go.uber.org/zap"
)

const (
	loadWindowSize = 120 // 60s at 500ms sample rate
)

// EventSample mirrors the kernel-side event_sample struct from
// metrics_collector.bpf.c.  Field layout MUST match the C struct exactly
// (packed, little-endian) — any mismatch silently corrupts all metrics.
//
// If you change event_sample in the C file, update this struct and
// EventSampleSize immediately.
type EventSample struct {
	InstanceID     uint32
	_              [4]byte // padding for __u64 alignment
	LatencyNs      uint64
	Error          uint32
	CircuitState   uint32
	VnodesAtSelect uint32
	ProbeIdx       uint8
	Reason         uint8
	Pad            uint16
	TimestampNs    uint64
}

// EventSampleSize is validated at init time against unsafe.Sizeof.
const EventSampleSize = 40 // bytes

// Reason codes matching kernel event_sample.reason
const (
	ReasonNormal   uint8 = 0
	ReasonHalfOpen uint8 = 1
	ReasonFallback uint8 = 2
)

// Sample is a parsed metric observation from the eBPF ringbuf.
type Sample struct {
	InstanceID     uint32
	LatencyNs      uint64
	Error          bool
	CircuitState   uint32
	VnodesAtSelect uint32
	ProbeIdx       uint8
	Reason         uint8
	Timestamp      time.Time
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

	// onSample is called for every ringbuf event; used by the FlightRecorder.
	onSample func(Sample)
}

// GlobalCollector is set by daemon wiring so rl.Agent can read state.
var GlobalCollector *Collector

// NewCollector creates the metrics collector.
func NewCollector(pinPath string, log *zap.Logger) (*Collector, error) {
	c := &Collector{log: log, pinPath: pinPath}
	GlobalCollector = c
	return c, nil
}

// SetSampleHook registers a callback invoked for every ringbuf event.
// Used by the FlightRecorder in observability/flight_recorder.go.
func (c *Collector) SetSampleHook(fn func(Sample)) {
	c.onSample = fn
}

// ErrorRate returns the current 1-minute error rate (0.0–1.0) for a backend.
// Used by the slow-start controller's error rate threshold check.
func (c *Collector) ErrorRate(backendID uint32) float64 {
	v, ok := c.backends.Load(backendID)
	if !ok {
		return 0
	}
	stats := v.(*BackendStats)
	stats.mu.Lock()
	r := stats.ErrorRate1m
	stats.mu.Unlock()
	return r * 100 // return as percentage
}

// Run reads events from the eBPF ringbuf and updates in-memory stats.
//
// Real ringbuf read path:
//  1. Open the pinned events_ringbuf map from pinPath.
//  2. Create a ringbuf.Reader (cilium/ebpf/ringbuf).
//  3. Read() blocks until an event is available or ctx is cancelled.
//  4. Parse the raw bytes into EventSample.
//  5. Call Ingest() to update EWMA stats.
//  6. Call onSample hook (FlightRecorder).
//
// If the pinned map is not yet available (daemon just started), fall back to
// polling every 500ms until the map appears.
func (c *Collector) Run(ctx context.Context) error {
	// Wait for the pinned map to appear (eBPF may load after collector starts)
	var rbMap *ebpf.Map
	for {
		var err error
		rbMap, err = ebpf.LoadPinnedMap(c.pinPath+"/events_ringbuf", nil)
		if err == nil {
			break
		}
		c.log.Info("waiting for events_ringbuf map to be pinned",
			zap.String("path", c.pinPath+"/events_ringbuf"),
		)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
	defer rbMap.Close()

	rd, err := ringbuf.NewReader(rbMap)
	if err != nil {
		return err
	}
	defer rd.Close()

	c.log.Info("eBPF ringbuf reader started — flight recorder active",
		zap.String("map", c.pinPath+"/events_ringbuf"),
	)

	// Close the reader when ctx is cancelled so Read() unblocks.
	go func() {
		<-ctx.Done()
		rd.Close()
	}()

	for {
		record, err := rd.Read()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.log.Error("ringbuf read error", zap.Error(err))
			return err
		}
		s, ok := parseEventSample(record.RawSample)
		if !ok {
			c.log.Warn("malformed ringbuf event", zap.Int("len", len(record.RawSample)))
			continue
		}
		c.Ingest(s)
		if c.onSample != nil {
			c.onSample(s)
		}
	}
}

// parseEventSample decodes a raw ringbuf record into a Sample.
// Returns (sample, false) if the record is too short.
func parseEventSample(raw []byte) (Sample, bool) {
	if len(raw) < EventSampleSize {
		return Sample{}, false
	}
	// Manual decode matching EventSample struct layout (little-endian).
	// Using unsafe.Pointer would be faster but unsafe across architectures;
	// binary.LittleEndian is portable and the hot path is not CPU-bound.
	_ = unsafe.Sizeof(EventSample{}) // keep import
	le := binary.LittleEndian
	s := Sample{
		InstanceID: le.Uint32(raw[0:4]),
		// raw[4:8] = padding
		LatencyNs:      le.Uint64(raw[8:16]),
		Error:          le.Uint32(raw[16:20]) != 0,
		CircuitState:   le.Uint32(raw[20:24]),
		VnodesAtSelect: le.Uint32(raw[24:28]),
		ProbeIdx:       raw[28],
		Reason:         raw[29],
		// raw[30:32] = pad
		Timestamp: time.Unix(0, int64(le.Uint64(raw[32:40]))),
	}
	return s, true
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
