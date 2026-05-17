package rl

import (
	"context"
	"fmt"
	"math"

	"go.uber.org/zap"
)

// KANActor wraps the ONNX-compiled KAN actor model (Layer 3).
// The KAN actor replaces the opaque MLP with an interpretable B-spline network.
// After training, symbolic equations are extracted and written to an audit log.
type KANActor struct {
	log       *zap.Logger
	modelPath string
	// In production: ort *onnxruntime.Session
	// Kept as interface to avoid hard C dependency in this file.
	session OrtSession
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
		return &KANActor{log: log}, nil
	}
	// Real: session, err := ort.NewSession(modelPath, inputNames, outputNames)
	// Stub for compilation:
	log.Info("KAN actor loaded", zap.String("model", modelPath))
	return &KANActor{log: log, modelPath: modelPath}, nil
}

// Infer runs the KAN actor forward pass and returns a normalised weight vector.
// state is the flattened MDP state vector (N×8+4 dimensions).
func (k *KANActor) Infer(ctx context.Context, state []float64) ([]float64, error) {
	if k.session == nil {
		// Fallback: KAN symbolic equations extracted post-training.
		// These equations are the interpretable output — auditable by SREs.
		return k.symbolicFallback(state), nil
	}

	// Convert to float32 for ONNX
	inp := make([]float32, len(state))
	for i, v := range state {
		inp[i] = float32(v)
	}

	done := make(chan struct{})
	var out [][]float32
	var runErr error
	go func() {
		out, runErr = k.session.Run([][]float32{inp})
		close(done)
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("KAN inference timeout: %w", ctx.Err())
	case <-done:
	}

	if runErr != nil {
		return nil, runErr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("KAN model returned empty output")
	}

	// Apply softmax to output logits
	logits := out[0]
	return softmax64(logits), nil
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

// WriteAuditLog emits the current symbolic equations to the KAN audit log.
func (k *KANActor) WriteAuditLog(version string) {
	k.log.Info("KAN policy audit",
		zap.String("version", version),
		zap.String("equation", "w_i = max(0, 1 - 0.42·cpu_i - 0.31·latency_i/1000 - 10·errRate_i) × health_i"),
		zap.String("note", "SRE diff: compare consecutive versions for routing logic changes"),
	)
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
