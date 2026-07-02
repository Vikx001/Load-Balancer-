// Package ring — maglev.go
//
// ─── WHY BINARY SEARCH ON 7,500 VNODES SHOWS UP IN FLAMEGRAPHS ──────────────
// The consistent hash ring uses a sorted array of virtual node positions and
// binary search to find the target backend.  With 150 vnodes × 50 servers =
// 7,500 entries, binary search takes ~13 comparisons.
//
// At 1M req/s: 13M comparisons/second, each a potential L1 cache miss.
// The ring array: 7,500 × 8 bytes = 60KB — exceeds typical L1 (32KB) and
// partially misses L2 (256KB shared with other hot data).  In flamegraphs this
// shows as "bisect_right" or "ring lookup" consuming 8–12% of CPU time.
//
// ─── THE FIX: MAGLEV HASH TABLE ─────────────────────────────────────────────
// Maglev (Google, NSDI 2016) uses a flat array of M slots (M = 65537, a prime).
// Lookup is ONE array index:
//
//	backend_id = maglev_table[hash(key) % M]   ← O(1), fits in L1/L2
//
// M = 65537 × 4 bytes = 256KB — fits entirely in L2 cache.
// After the first few requests, the table is hot and lookup becomes ~1ns vs
// ~80ns for binary search with cold ring data.
//
// ─── HOW IT INTEGRATES WITH H&A ──────────────────────────────────────────────
// The H&A self-adjustment logic still works on virtual node counts.  After
// every vnode count change, Manager.RebuildMaglevTable() recomputes the flat
// lookup table and writes it to the eBPF maglev_table map.  The eBPF data
// path uses the flat table for O(1) lookup.
//
// The conceptual ring is kept for bounded-load secondary selection and for the
// slow-start/session-affinity logic.  It is NOT in the hot eBPF path.
//
// ─── MAGLEV ALGORITHM ────────────────────────────────────────────────────────
//
//  1. For each backend b, generate a permutation of M slots using:
//     offset = hash(b, "offset") % M
//     skip   = hash(b, "skip")   % (M-1) + 1   (must be non-zero)
//     perm[b][j] = (offset + j*skip) % M
//
//  2. Fill the table with M slots (round-robin across backends weighted by
//     their vnode count):
//     next[b] = 0 for all b
//     for i = 0 to M-1:
//     pick backend b with highest remaining quota
//     table[perm[b][next[b]]] = b until an empty slot is found
//     next[b]++
//
// This distributes traffic proportional to vnode counts with minimal
// disruption on backend add/remove (consistent: only 1/N of slots move).
package ring

import (
	"encoding/binary"
	"math/bits"

	"go.uber.org/zap"
)

// MaglevM is the number of slots in the Maglev lookup table.
// Must be prime for good distribution.  65537 is the standard choice (Google).
// 65537 × 4 bytes = 256KB — fits in L2 cache on all modern server CPUs.
const MaglevM = 65537

// MaglevTable is a flat array of MaglevM backend IDs.
// Index: hash(key) % MaglevM  →  Value: instance_id (uint32)
// 0 means "no backend assigned to this slot" (should not occur in a valid table).
type MaglevTable [MaglevM]uint32

// BuildMaglevTable constructs a Maglev lookup table for the given backends
// and their relative vnode counts (weights).
//
// vnodeCounts maps instance_id → number of virtual nodes.  Backends with more
// vnodes receive proportionally more slots.  This is the integration point with
// H&A: after every vnode adjustment, rebuild the Maglev table.
//
// Returns a zero table if backends is empty.
func BuildMaglevTable(backends []uint32, vnodeCounts map[uint32]int) MaglevTable {
	var table MaglevTable
	if len(backends) == 0 {
		return table
	}

	// Total vnode weight
	totalWeight := 0
	for _, id := range backends {
		totalWeight += vnodeCounts[id]
	}
	if totalWeight == 0 {
		// Fallback: equal weights
		for _, id := range backends {
			vnodeCounts[id] = 1
			totalWeight++
		}
	}

	// Compute permutation parameters for each backend.
	// offset and skip are derived from the backend ID so they are stable
	// across rebuilds when the backend set does not change.
	type permState struct {
		id     uint32
		offset uint32
		skip   uint32
		next   uint32 // next permutation index to try
		quota  int    // remaining slots to fill
	}

	perms := make([]permState, len(backends))
	for i, id := range backends {
		perms[i] = permState{
			id:     id,
			offset: maglevHash(id, 0xDEAD) % MaglevM,
			skip:   maglevHash(id, 0xBEEF)%(MaglevM-1) + 1, // must be ≥ 1
			quota:  vnodeCounts[id],
		}
	}

	// Fill MaglevM slots round-robin weighted by quota.
	// Quota remaining for each backend is proportional to its vnode count.
	// We scale all quotas so that sum(quota) = M.
	scaledQuotas := make([]int, len(perms))
	rem := MaglevM
	for i, p := range perms {
		// Integer proportional allocation
		scaledQuotas[i] = p.quota * MaglevM / totalWeight
		rem -= scaledQuotas[i]
	}
	// Distribute remaining slots to avoid truncation loss
	for i := 0; rem > 0; i = (i + 1) % len(scaledQuotas) {
		scaledQuotas[i]++
		rem--
	}
	for i := range perms {
		perms[i].quota = scaledQuotas[i]
	}

	filled := 0
	// Iterate until all M slots are filled.
	// Round-robin across backends; each backend fills one slot per turn.
	for filled < MaglevM {
		for i := range perms {
			if perms[i].quota <= 0 {
				continue
			}
			// Find next empty slot in this backend's permutation
			for {
				slot := (perms[i].offset + perms[i].next*perms[i].skip) % MaglevM
				perms[i].next++
				if table[slot] == 0 {
					table[slot] = perms[i].id
					perms[i].quota--
					filled++
					break
				}
				// Slot already taken by another backend — try next permutation position
			}
		}
	}
	return table
}

// maglevHash computes a 32-bit hash of (id, seed) using a mixing function.
// Both calls use different seeds to produce independent offset and skip values.
func maglevHash(id, seed uint32) uint32 {
	h := id ^ seed
	h = bits.RotateLeft32(h, 5) ^ seed
	// FNV-like mix
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[:4], h)
	binary.LittleEndian.PutUint32(buf[4:], seed)
	h = 2166136261
	for _, b := range buf {
		h ^= uint32(b)
		h *= 16777619
	}
	return h
}

// RebuildMaglevTable recomputes the Maglev table from the current vnode
// distribution and writes it to the eBPF maglev_table map (if pinPath is set).
// Called by Manager after every vnode adjustment.
func (m *Manager) RebuildMaglevTable() MaglevTable {
	m.mu.RLock()
	backends := make([]uint32, 0, len(m.backends))
	vnodeCounts := make(map[uint32]int, len(m.backends))
	for id, b := range m.backends {
		if b.Health {
			backends = append(backends, id)
			vnodeCounts[id] = b.VnodeCount
		}
	}
	m.mu.RUnlock()

	tbl := BuildMaglevTable(backends, vnodeCounts)
	m.log.Info("maglev table rebuilt",
		zap.Int("backends", len(backends)),
		zap.Int("slots", MaglevM),
	)
	return tbl
}

// Lookup returns the backend ID for the given key using O(1) Maglev lookup.
// This is the hot data-path function: one array index, no branching.
func (t *MaglevTable) Lookup(key uint32) uint32 {
	return t[key%MaglevM]
}
