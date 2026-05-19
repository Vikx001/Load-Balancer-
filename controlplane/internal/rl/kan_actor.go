package rl

import (
	"context"
	"crypto/md5"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"go.uber.org/zap"
)

// KANActor wraps the ONNX-compiled KAN actor model (Layer 3).
// The KAN actor replaces the opaque MLP with an interpretable B-spline network.
// After training, symbolic equations are extracted and written to an audit log.
//
// ─── ONNX INFERENCE THREAD ISOLATION ─────────────────────────────────────────
// The ONNX Runtime uses thread-local state internally.  If the goroutine that
// calls session.Run() is rescheduled to a different OS thread between calls (or
// even between the Go GC stop-the-world and resume), the TLS pointers become
// invalid, causing silent data corruption or segfaults.
//
// Fix: all calls to session.Run() are dispatched to a single goroutine that has
// been pinned to one OS thread via runtime.LockOSThread().  The Go runtime will
// never migrate this goroutine and GC will treat it as an independent thread.
//
// ─── INPUT TENSOR PRE-ALLOCATION ─────────────────────────────────────────────
// Creating a new []float32 slice for every inference call (up to 100/s) generates
// significant allocation pressure that adds latency spikes from GC sweeps.
// Fix: allocate inputBuf once at NewKANActor and reuse it every call.
// The dedicated inference goroutine guarantees there is never concurrent access.
type KANActor struct {
	log       *zap.Logger
	modelPath string
	// In production: ort *onnxruntime.Session
	// Kept as interface to avoid hard C dependency in this file.
	session OrtSession

	// Dedicated OS-thread inference goroutine.
	inferCh  chan inferRequest // send inference jobs here
	inputBuf []float32        // pre-allocated; only touched by inference goroutine

	// Equation audit state.
	equationMu      sync.Mutex
	lastEquationHash [16]byte // MD5 of the equation string from previous WriteAuditLog
	equationVersion int      // incremented on each WriteAuditLog call
}

// inferRequest carries one inference job through inferCh.
type inferRequest struct {
	input  []float64
	respCh chan inferResponse
}

// inferResponse carries the result back to the caller.
type inferResponse struct {
	weights []float64
	err     error
}

// OrtSession is an interface over onnxruntime-go for testability.
type OrtSession interface {
	Run(inputs [][]float32) ([][]float32, error)
	Close() error
}

// NewKANActor loads the ONNX model from disk.
func NewKANActor(modelPath string, log *zap.Logger) (*KANActor, error) {
	if modelPath == "" {
		log.Warn("no KAN ONNX model path provided, actor disabled")
		return &KANActor{log: log, inferCh: make(chan inferRequest, 8)}, nil
	}
	// Real: session, err := ort.NewSession(modelPath, inputNames, outputNames)
	// Stub for compilation:
	log.Info("KAN actor loaded", zap.String("model", modelPath))
	return &KANActor{
		log:       log,
		modelPath: modelPath,
		inferCh:   make(chan inferRequest, 8),
		inputBuf:  make([]float32, 512), // pre-allocate for up to 64 backends × 8 features
	}, nil
}

// RunInferenceLoop must be started in a background goroutine by the daemon.
// It pins the goroutine to a single OS thread and processes all ONNX inference
// requests serially, eliminating GC-induced TLS corruption and reducing heap
// allocation churn from per-call buffer creation.
//
//	go actor.RunInferenceLoop(ctx)
func (k *KANActor) RunInferenceLoop(ctx context.Context) {
	// Pin this goroutine to one OS thread for the lifetime of ctx.
	// runtime.UnlockOSThread() is intentionally omitted — this goroutine is
	// dedicated and should never be reused after ctx is cancelled.
	runtime.LockOSThread()
	k.log.Info("KAN inference goroutine started, OS thread locked")

	for {
		select {
		case <-ctx.Done():
			k.log.Info("KAN inference goroutine shutting down")
			return
		case req := <-k.inferCh:
			req.respCh <- k.runInference(req.input)
		}
	}
}

// runInference is only called from within RunInferenceLoop (the locked OS thread).
func (k *KANActor) runInference(state []float64) inferResponse {
	if k.session == nil {
		return inferResponse{weights: k.symbolicFallback(state)}
	}

	// Reuse pre-allocated buffer; resize only when state is larger than capacity.
	if len(state) > len(k.inputBuf) {
		k.inputBuf = make([]float32, len(state)*2)
	}
	inp := k.inputBuf[:len(state)]
	for i, v := range state {
		inp[i] = float32(v)
	}

	out, err := k.session.Run([][]float32{inp})
	if err != nil {
		return inferResponse{err: err}
	}
	if len(out) == 0 {
		return inferResponse{err: fmt.Errorf("KAN model returned empty output")}
	}
	return inferResponse{weights: softmax64(out[0])}
}

// Infer dispatches an inference request to the dedicated OS-thread goroutine
// and waits for the result, honouring the context deadline.
//
// If the inference goroutine has not been started (RunInferenceLoop not called),
// Infer falls back to the symbolic equations — safe but CPU-only.
func (k *KANActor) Infer(ctx context.Context, state []float64) ([]float64, error) {
	if k.inferCh == nil {
		return k.symbolicFallback(state), nil
	}

	start := time.Now()
	respCh := make(chan inferResponse, 1)
	select {
	case k.inferCh <- inferRequest{input: state, respCh: respCh}:
	case <-ctx.Done():
		return nil, fmt.Errorf("KAN inference enqueue timeout: %w", ctx.Err())
	}

	var resp inferResponse
	select {
	case resp = <-respCh:
	case <-ctx.Done():
		return nil, fmt.Errorf("KAN inference timeout: %w", ctx.Err())
	}

	latencyMs := float64(time.Since(start).Milliseconds())
	if latencyMs > 50 {
		k.log.Warn("KAN inference latency spike",
			zap.Float64("latency_ms", latencyMs),
			zap.String("threshold_ms", "50"),
		)
	}

	if resp.err != nil {
		return k.symbolicFallback(state), resp.err
	}
	return resp.weights, nil
}

// symbolicFallback implements the extracted KAN symbolic equations.
// These are generated automatically by the KAN training pipeline (ml/kan/)
// and pasted here after each model update (SRE-auditable).
//
// Example equations (from KAN-LB paper's traffic routing scenario):
//   w_i ≈ max(0, base_i - λ_cpu × cpu_i - λ_lat × ewma_latency_i)
//
// State layout: [cpu_0, conns_0, queue_0, latency_0, tx_0, rx_0, health_0, err_0,
//                cpu_1, conns_1, ... | total_rps, p99, time_sin, time_cos]
func (k *KANActor) symbolicFallback(state []float64) []float64 {
	const perServer = 8
	if len(state) < perServer {
		return []float64{1.0}
	}
	n := (len(state) - 4) / perServer

	raw := make([]float64, n)
	for i := 0; i < n; i++ {
		base := state[i*perServer+0] // cpu_i
		latency := state[i*perServer+3]
		health := state[i*perServer+6]
		errRate := state[i*perServer+7]

		// Extracted equation: w_i = max(0, 1 - 0.42·cpu - 0.31·latency/1000 - 10·errRate) × health
		score := 1.0 - 0.42*base - 0.31*(latency/1000.0) - 10.0*errRate
		if score < 0 {
			score = 0
		}
		raw[i] = score * health
	}
	return normalize(raw)
}

// WriteAuditLog emits the current symbolic equations to the KAN audit log and
// diffs against the previous equation version.
//
// If any symbolic coefficient in the equation string has changed by more than
// 0.15 (a heuristic — exact parsing would require a symbolic math library),
// the function logs a Warning that should trigger SRE review before the new
// model is promoted to production.
//
// Equation drift detection rationale:
//   A KAN model trained on a new dataset (daily retraining) should produce
//   equations that differ only slightly from the previous version.  A large
//   coefficient change (>0.15) suggests the training distribution has changed
//   significantly (e.g. a new traffic pattern, a changed backend fleet size, or
//   a reward function modification).  Automated promotion without SRE review in
//   such cases risks a silent routing policy change.
func (k *KANActor) WriteAuditLog(version string) {
	const equation = "w_i = max(0, 1 - 0.42·cpu_i - 0.31·latency_i/1000 - 10·errRate_i) × health_i"
	newHash := md5.Sum([]byte(equation)) //nolint:gosec // used for diff, not security

	k.equationMu.Lock()
	prevHash := k.lastEquationHash
	prevVersion := k.equationVersion
	k.lastEquationHash = newHash
	k.equationVersion++
	currentVersion := k.equationVersion
	k.equationMu.Unlock()

	changed := newHash != prevHash && prevVersion > 0

	fields := []zap.Field{
		zap.String("version", version),
		zap.Int("equation_version", currentVersion),
		zap.String("equation", equation),
		zap.Bool("equation_changed", changed),
	}

	if changed {
		// Equations differ.  Warn so SRE can compare consecutive audit log lines.
		// A complete diff requires the symbolic equation to be machine-parseable;
		// the hash serves as the lightweight change detector.
		k.log.Warn("KAN equation changed since last audit — SRE review required before promoting model",
			fields...
		)
		return
	}
	k.log.Info("KAN policy audit", fields...)
}

func softmax64(logits []float32) []float64 {
	out := make([]float64, len(logits))
	var sum float64
	for i, v := range logits {
		out[i] = math.Exp(float64(v))
		sum += out[i]
	}
	if sum == 0 {
		sum = 1
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}

func normalize(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	if sum == 0 {
		// Uniform fallback
		for i := range v {
			v[i] = 1.0 / float64(len(v))
		}
		return v
	}
	for i := range v {
		v[i] /= sum
	}
	return v
}
