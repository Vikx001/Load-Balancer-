// Package rl — model_store.go
//
// ─── WHY MODEL VERSIONING IS LIFE-CRITICAL ───────────────────────────────────
// A retrained RL model that has diverged during training (reward hacking, data
// distribution shift, or simply a bad hyperparameter run) can cause cascading
// failures within seconds of deployment:
//
//	New model pushes 80% weight to backend-3
//	→ backend-3 saturates in ~10s at 50k RPS
//	→ 5xx rate spikes; circuit breaker trips
//	→ CBF projects weights away from backend-3
//	→ weight bounces to backend-1; backend-1 saturates
//	→ cascade failure; SRE wakes at 3am
//
// Without versioning and rollback, recovery requires:
//  1. Diagnose the model is bad (5-10 minutes)
//  2. Retrain from last checkpoint (30 min - 2 hours)
//  3. Deploy manually (5-10 minutes)
//
// With versioning and hot-reload, recovery is:
//
//	$ omegalb model rollback --to=v1.3.1    # < 5 seconds, zero restart
//
// ─── PROMOTION STAGES ────────────────────────────────────────────────────────
//
//	staging  → canary (5% traffic via shadow ring)
//	canary   → production (if error rate delta < 0.5% over 10 minutes)
//	production → rollback (operator command or automatic on error threshold)
//
// The ModelStore does NOT implement traffic splitting itself.  That is the
// ring manager's job.  The store only manages which model file is active.
package rl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.uber.org/zap"
)

// ModelStage tracks where in the promotion pipeline a model is.
type ModelStage string

const (
	StageShadow     ModelStage = "shadow"     // loaded but serving 0 traffic
	StageCanary     ModelStage = "canary"     // serving 5% of traffic
	StageProduction ModelStage = "production" // fully promoted
	StageRolledBack ModelStage = "rolled_back"
)

// ModelVersion describes a stored model revision.
type ModelVersion struct {
	// Version is a semantic version string, e.g. "v1.4.2".
	Version string `json:"version"`
	// Path is the absolute path to the ONNX file on disk.
	Path string `json:"path"`
	// Stage is the current promotion stage.
	Stage ModelStage `json:"stage"`
	// Checksum is the SHA-256 hex digest of the ONNX file.
	// Validated on Pull() to detect corruption or tampering.
	Checksum  string    `json:"checksum"`
	CreatedAt time.Time `json:"created_at"`
}

// ModelStore manages versioned ONNX model files on local disk.
// The store directory layout is:
//
//	<base_path>/
//	  v1.3.1/
//	    model.onnx
//	  v1.4.2/
//	    model.onnx
//	  registry.json    ← list of all versions with metadata
//
// The registry.json is the source of truth for version metadata.
// It is written atomically (write temp, rename) on every Push.
type ModelStore struct {
	basePath string
	log      *zap.Logger
}

// NewLocalModelStore creates a ModelStore rooted at basePath.
// The directory is created if it does not exist.
func NewLocalModelStore(basePath string, log *zap.Logger) (*ModelStore, error) {
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return nil, fmt.Errorf("model store: mkdir %s: %w", basePath, err)
	}
	return &ModelStore{basePath: basePath, log: log}, nil
}

// Push copies srcPath into the store as the given version and updates the
// registry.  The version must not already exist; returns an error if it does.
// The file is SHA-256 checked before committing.
func (s *ModelStore) Push(version string, stage ModelStage, srcPath string) (ModelVersion, error) {
	existing, _ := s.List()
	for _, mv := range existing {
		if mv.Version == version {
			return ModelVersion{}, fmt.Errorf("model store: version %s already exists; bump the version or delete it first", version)
		}
	}

	// Create version directory
	destDir := filepath.Join(s.basePath, version)
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return ModelVersion{}, fmt.Errorf("model store: mkdir %s: %w", destDir, err)
	}
	destPath := filepath.Join(destDir, "model.onnx")

	// Copy and checksum
	checksum, err := copyAndChecksum(srcPath, destPath)
	if err != nil {
		return ModelVersion{}, fmt.Errorf("model store push: %w", err)
	}

	mv := ModelVersion{
		Version:   version,
		Path:      destPath,
		Stage:     stage,
		Checksum:  checksum,
		CreatedAt: time.Now().UTC(),
	}

	// Write registry
	if err := s.upsertRegistry(mv); err != nil {
		return ModelVersion{}, err
	}
	s.log.Info("model pushed to store",
		zap.String("version", version),
		zap.String("stage", string(stage)),
		zap.String("checksum", checksum[:8]+"..."),
	)
	return mv, nil
}

// Pull returns the ModelVersion for the given version string and validates the
// file checksum before returning the path.
func (s *ModelStore) Pull(version string) (ModelVersion, error) {
	all, err := s.List()
	if err != nil {
		return ModelVersion{}, err
	}
	for _, mv := range all {
		if mv.Version != version {
			continue
		}
		// Validate checksum on pull to detect corruption or tampering.
		got, err := checksumFile(mv.Path)
		if err != nil {
			return ModelVersion{}, fmt.Errorf("model store pull %s: checksum error: %w", version, err)
		}
		if got != mv.Checksum {
			return ModelVersion{}, fmt.Errorf(
				"model store pull %s: checksum mismatch (stored=%s, got=%s); "+
					"file may be corrupted or tampered with", version, mv.Checksum[:8], got[:8])
		}
		return mv, nil
	}
	return ModelVersion{}, fmt.Errorf("model store: version %s not found", version)
}

// List returns all stored versions sorted by creation time (newest first).
func (s *ModelStore) List() ([]ModelVersion, error) {
	path := filepath.Join(s.basePath, "registry.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // empty store
	}
	if err != nil {
		return nil, fmt.Errorf("model store: read registry: %w", err)
	}
	var versions []ModelVersion
	if err := json.Unmarshal(data, &versions); err != nil {
		return nil, fmt.Errorf("model store: parse registry: %w", err)
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].CreatedAt.After(versions[j].CreatedAt)
	})
	return versions, nil
}

// Latest returns the most recently pushed version.
func (s *ModelStore) Latest() (ModelVersion, error) {
	all, err := s.List()
	if err != nil {
		return ModelVersion{}, err
	}
	if len(all) == 0 {
		return ModelVersion{}, fmt.Errorf("model store: no versions available")
	}
	return all[0], nil
}

// Promote updates the stage of a version (e.g. shadow → canary → production).
func (s *ModelStore) Promote(version string, newStage ModelStage) error {
	all, err := s.List()
	if err != nil {
		return err
	}
	found := false
	for i, mv := range all {
		if mv.Version == version {
			all[i].Stage = newStage
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model store: version %s not found", version)
	}
	return s.writeRegistry(all)
}

func (s *ModelStore) upsertRegistry(mv ModelVersion) error {
	all, _ := s.List()
	for i, existing := range all {
		if existing.Version == mv.Version {
			all[i] = mv
			return s.writeRegistry(all)
		}
	}
	all = append(all, mv)
	return s.writeRegistry(all)
}

func (s *ModelStore) writeRegistry(versions []ModelVersion) error {
	data, err := json.MarshalIndent(versions, "", "  ")
	if err != nil {
		return fmt.Errorf("model store: marshal registry: %w", err)
	}
	tmp := filepath.Join(s.basePath, "registry.json.tmp")
	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return fmt.Errorf("model store: write registry tmp: %w", err)
	}
	dest := filepath.Join(s.basePath, "registry.json")
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("model store: atomic rename registry: %w", err)
	}
	return nil
}

// copyAndChecksum copies src to dst and returns the SHA-256 hex digest.
func copyAndChecksum(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("open src %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return "", fmt.Errorf("open dst %s: %w", dst, err)
	}
	defer out.Close()

	h := sha256.New()
	w := io.MultiWriter(out, h)
	if _, err := io.Copy(w, in); err != nil {
		return "", fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
