// Package ring implements Layer 1: Hash & Adjust demand-aware consistent hashing.
// Reference: H&A paper, OPODIS 2024.
package ring

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
)

const (
	ringSize         = math.MaxUint32
	defaultVnodes    = 150
	adjustEveryN     = 100
	betaBounded      = 1.25
	adjustThreshold  = 1.30 // 30% above mean triggers H&A move
	proactiveCapPct  = 0.75 // pre-migrate if predicted load > 75%
	proactiveLookAhd = 30.0 // seconds
	proactiveCutPct  = 0.15 // reduce vnodes by 15% on proactive trigger
)

// Backend represents a backend server.
type Backend struct {
	ID          uint32
	IP          [4]byte
	Port        uint16
	VnodeCount  int
	ActiveReqs  int64
	CapacityMax int64 // max active connections / CPU threshold
	Health      bool
	// Stateful marks a backend as serving stateful traffic (auth sessions,
	// WebSockets, DB connections).  H&A vnode adjustment is suppressed for
	// stateful backends to preserve session affinity.  Use the AffinityTable
	// for routing; only adjust weight via traffic mirroring.
	Stateful bool
}

// VNode is a position on the ring.
type vnode struct {
	pos     uint32
	backend *Backend
}

// Manager owns the H&A ring and pushes updates to eBPF maps.
type Manager struct {
	mu        sync.RWMutex
	cfg       config.RingConfig
	log       *zap.Logger
	backends  map[uint32]*Backend  // id → backend
	ring      []vnode              // sorted by pos
	reqCount  int64                // rolling counter for adjust trigger
	affinity  *AffinityTable       // session sticky routing (stateful services)
	slowStart *SlowStartController // thundering herd protection on recovery
}

// NewManager constructs a ring Manager.
func NewManager(cfg config.RingConfig, log *zap.Logger) (*Manager, error) {
	if cfg.VnodesPerServer == 0 {
		cfg.VnodesPerServer = defaultVnodes
	}
	if cfg.BoundedLoadBeta == 0 {
		cfg.BoundedLoadBeta = betaBounded
	}
	return &Manager{
		cfg:      cfg,
		log:      log,
		backends: make(map[uint32]*Backend),
		affinity: NewAffinityTable(0, 0, log), // 30min TTL, 1M sessions
		slowStart: newSlowStartController(
			cfg.SlowStartBatchSize,
			cfg.SlowStartIntervalS,
			cfg.SlowStartMaxErrorRatePct,
			log,
		),
	}, nil
}

// AddBackend inserts a backend and builds its virtual nodes.
func (m *Manager) AddBackend(b *Backend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.backends[b.ID]; !exists {
		b.VnodeCount = m.cfg.VnodesPerServer
	}
	m.backends[b.ID] = b
	m.rebuild()
	m.log.Info("backend added to ring",
		zap.Uint32("id", b.ID),
		zap.Int("vnodes", b.VnodeCount),
	)
}

// BackendInfo returns a copy of the Backend struct for the given ID.
// Returns nil if the backend is not registered.
func (m *Manager) BackendInfo(id uint32) *Backend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.backends[id]
	if !ok {
		return nil
	}
	copy := *b
	return &copy
}

// RemoveBackend removes a backend from the ring.
func (m *Manager) RemoveBackend(id uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.backends, id)
	m.rebuild()
	m.log.Info("backend removed from ring", zap.Uint32("id", id))
}

// SetHealth marks a backend healthy or unhealthy.
func (m *Manager) SetHealth(id uint32, healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.backends[id]; ok {
		b.Health = healthy
		m.rebuild()
	}
}

// Route returns the instance_id of the backend for a given request hash key.
// Implements H&A: consistent hash + bounded load check + self-adjustment.
func (m *Manager) Route(key uint32) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.ring) == 0 {
		return 0, fmt.Errorf("no backends in ring")
	}

	pos := murmur3Finalise(key)
	idx := m.bisectRight(pos)
	totalConns := m.totalActiveReqs()
	numBackends := int64(len(m.backends))

	var chosen *Backend
	for probe := 0; probe < len(m.ring); probe++ {
		vn := m.ring[(idx+probe)%len(m.ring)]
		b := vn.backend
		if !b.Health {
			continue
		}
		// Bounded load check: reject if active > β × mean
		mean := float64(totalConns) / float64(max64(numBackends, 1))
		if float64(b.ActiveReqs) > m.cfg.BoundedLoadBeta*mean {
			continue
		}
		chosen = b
		break
	}

	if chosen == nil {
		// All overloaded — pick least loaded
		chosen = m.leastLoaded()
	}
	if chosen == nil {
		return 0, fmt.Errorf("no healthy backends")
	}

	chosen.ActiveReqs++
	m.reqCount++
	if m.reqCount%int64(m.cfg.AdjustEveryN) == 0 {
		m.adjust()
	}

	return chosen.ID, nil
}

// ReleaseConn decrements the active request count after a response.
func (m *Manager) ReleaseConn(id uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.backends[id]; ok && b.ActiveReqs > 0 {
		b.ActiveReqs--
	}
}

// SortedPositions returns the sorted ring position slice for eBPF map sync.
func (m *Manager) SortedPositions() (positions []uint32, ids []uint32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, vn := range m.ring {
		positions = append(positions, vn.pos)
		ids = append(ids, vn.backend.ID)
	}
	return
}

// ProactiveAdjust: Layer 5 — pre-migrate before saturation.
// loadWindow is a map[backendID][]float64 of recent load samples (last 60s).
func (m *Manager) ProactiveAdjust(loadWindow map[uint32][]float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, samples := range loadWindow {
		b, ok := m.backends[id]
		if !ok || !b.Health {
			continue
		}
		slope := linearSlope(samples)
		currentLoad := 0.0
		if len(samples) > 0 {
			currentLoad = samples[len(samples)-1]
		}
		predicted := currentLoad + slope*proactiveLookAhd
		cap := float64(b.CapacityMax)
		if cap == 0 {
			cap = 1000
		}
		if predicted/cap > proactiveCapPct && b.VnodeCount > 1 {
			reduction := int(math.Ceil(float64(b.VnodeCount) * proactiveCutPct))
			if reduction < 1 {
				reduction = 1
			}
			b.VnodeCount -= reduction
			m.log.Info("proactive vnode reduction",
				zap.Uint32("backend", id),
				zap.Float64("predicted_load", predicted),
				zap.Int("new_vnodes", b.VnodeCount),
			)
		}
	}
	m.rebuild()
}

// ─── Internal helpers ──────────────────────────────────────────────────────

// rebuild regenerates the sorted ring from current backend vnode counts.
// Must be called under mu.Lock().
func (m *Manager) rebuild() {
	m.ring = m.ring[:0]
	for _, b := range m.backends {
		if !b.Health {
			continue
		}
		for i := 0; i < b.VnodeCount; i++ {
			key := fmt.Sprintf("%d:%d", b.ID, i)
			pos := murmur3_32([]byte(key))
			m.ring = append(m.ring, vnode{pos: pos, backend: b})
		}
	}
	sort.Slice(m.ring, func(i, j int) bool {
		return m.ring[i].pos < m.ring[j].pos
	})
}

// adjust implements the H&A self-adjustment: move a vnode from the most
// loaded server toward the least loaded if the most loaded exceeds threshold.
// Must be called under mu.Lock().
// NOTE: Stateful backends are excluded from vnode adjustment — their vnode
// count is never changed by H&A.  Adjusting stateful backends would silently
// break session affinity for in-flight sessions.
func (m *Manager) adjust() {
	mean := float64(m.totalActiveReqs()) / float64(max64(int64(len(m.backends)), 1))
	var hottest, coolest *Backend
	for _, b := range m.backends {
		if !b.Health || b.Stateful {
			continue // never adjust stateful backends
		}
		if hottest == nil || b.ActiveReqs > hottest.ActiveReqs {
			hottest = b
		}
		if coolest == nil || b.ActiveReqs < coolest.ActiveReqs {
			coolest = b
		}
	}
	if hottest == nil || coolest == nil || hottest == coolest {
		return
	}
	if float64(hottest.ActiveReqs) > m.cfg.AdjustThreshold*mean {
		if hottest.VnodeCount > 1 {
			hottest.VnodeCount--
			coolest.VnodeCount++
			m.rebuild()
			m.log.Debug("H&A ring adjusted",
				zap.Uint32("from", hottest.ID),
				zap.Uint32("to", coolest.ID),
			)
		}
	}
}

func (m *Manager) bisectRight(pos uint32) int {
	lo, hi := 0, len(m.ring)
	for lo < hi {
		mid := (lo + hi) / 2
		if m.ring[mid].pos <= pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= len(m.ring) {
		return 0
	}
	return lo
}

func (m *Manager) totalActiveReqs() int64 {
	var total int64
	for _, b := range m.backends {
		total += b.ActiveReqs
	}
	return total
}

func (m *Manager) leastLoaded() *Backend {
	var least *Backend
	for _, b := range m.backends {
		if !b.Health {
			continue
		}
		if least == nil || b.ActiveReqs < least.ActiveReqs {
			least = b
		}
	}
	return least
}

// murmur3_32 is a simple MurmurHash3 for string keys.
func murmur3_32(data []byte) uint32 {
	var h uint32 = 0x9747b28c
	for len(data) >= 4 {
		k := binary.LittleEndian.Uint32(data[:4])
		k *= 0xcc9e2d51
		k = (k << 15) | (k >> 17)
		k *= 0x1b873593
		h ^= k
		h = (h << 13) | (h >> 19)
		h = h*5 + 0xe6546b64
		data = data[4:]
	}
	var tail uint32
	switch len(data) {
	case 3:
		tail |= uint32(data[2]) << 16
		fallthrough
	case 2:
		tail |= uint32(data[1]) << 8
		fallthrough
	case 1:
		tail |= uint32(data[0])
		tail *= 0xcc9e2d51
		tail = (tail << 15) | (tail >> 17)
		tail *= 0x1b873593
		h ^= tail
	}
	return murmur3Finalise(h ^ uint32(len(data)))
}

func murmur3Finalise(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

func linearSlope(samples []float64) float64 {
	n := float64(len(samples))
	if n < 2 {
		return 0
	}
	// Use last min(10, n) samples
	start := 0
	if len(samples) > 10 {
		start = len(samples) - 10
	}
	pts := samples[start:]
	n = float64(len(pts))
	var sumX, sumY, sumXY, sumXX float64
	for i, y := range pts {
		x := float64(i)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / denom
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
