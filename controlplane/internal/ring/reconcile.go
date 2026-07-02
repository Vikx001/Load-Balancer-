package ring

import (
	"fmt"

	"github.com/cilium/ebpf"
	"go.uber.org/zap"
)

// ReconcileFromEBPF reads the pinned eBPF maps and reconstructs the in-memory
// ring to match the kernel's current routing state.
//
// ─── WHY RECONCILIATION IS REQUIRED ─────────────────────────────────────────
// After a daemon restart, the in-memory ring starts empty.  The eBPF maps in
// the kernel still contain the last routing table the previous daemon pushed.
// If the daemon rebuilds from its empty state (e.g. re-reading config), it may
// produce different vnode assignments than the kernel is using.
//
// The eBPF map is the single source of truth because:
//
//	a) It is persistent across daemon restarts (kernel memory survives process death).
//	b) It is what the kernel is actually using to make routing decisions.
//	c) The WAL may have entries the previous daemon wrote but never pushed.
//
// Reconciliation procedure:
//  1. Open ha_ring_map (pinned): position → instance_id
//  2. Open instance_registry (pinned): instance_id → backend_entry
//  3. Count how many ring positions map to each instance_id (= vnode count).
//  4. For each instance_id, look up backend IP/port from instance_registry.
//  5. Rebuild in-memory backends map and ring from these counts.
//
// This function must be called before Run() on daemon startup.
func (m *Manager) ReconcileFromEBPF(pinPath string, log *zap.Logger) error {
	// Open pinned eBPF maps by filesystem path.
	ringMap, err := ebpf.LoadPinnedMap(pinPath+"/ha_ring_map",
		&ebpf.LoadPinOptions{})
	if err != nil {
		return fmt.Errorf("open pinned ha_ring_map: %w; "+
			"if this is a fresh start, skip reconciliation", err)
	}
	defer ringMap.Close()

	regMap, err := ebpf.LoadPinnedMap(pinPath+"/instance_registry",
		&ebpf.LoadPinOptions{})
	if err != nil {
		return fmt.Errorf("open pinned instance_registry: %w", err)
	}
	defer regMap.Close()

	// Count vnode occurrences per instance_id.
	vnodeCounts := make(map[uint32]int)
	var pos, instID uint32
	ringIter := ringMap.Iterate()
	for ringIter.Next(&pos, &instID) {
		vnodeCounts[instID]++
	}
	if err := ringIter.Err(); err != nil {
		return fmt.Errorf("iterate ha_ring_map: %w", err)
	}

	// backendEntry mirrors the eBPF struct layout in route_manager.bpf.c.
	// Field order must match the C struct exactly.
	type backendEntry struct {
		IP            [4]byte
		Port          uint16
		Health        uint8
		Pad           uint8
		VnodeCount    uint32
		ActiveReqs    uint32
		EWMALatencyNs uint32
	}

	// Rebuild in-memory backends from kernel state.
	m.mu.Lock()
	defer m.mu.Unlock()

	m.backends = make(map[uint32]*Backend)
	for instID, vcount := range vnodeCounts {
		var be backendEntry
		if err := regMap.Lookup(instID, &be); err != nil {
			log.Warn("reconcile: instance_registry lookup miss",
				zap.Uint32("instance_id", instID),
				zap.Error(err),
			)
			continue
		}
		m.backends[instID] = &Backend{
			ID:         instID,
			IP:         be.IP,
			Port:       be.Port,
			VnodeCount: vcount,
			Health:     be.Health == 1,
		}
	}

	m.rebuild()

	log.Info("ring reconciled from eBPF map state",
		zap.Int("backends_restored", len(m.backends)),
		zap.Int("ring_size", len(m.ring)),
	)
	return nil
}
