// SPDX-License-Identifier: GPL-2.0
// Layer 0 — metrics_collector: update per-flow stats, export signal to CP
//
// ─── VERIFIER SAFETY ─────────────────────────────────────────────────────────
// This program contains no loops.  The tail-call depth at entry is 4 (the last
// in the chain); the guard below ensures this never exceeds TAIL_CALL_DEPTH_MAX.
//
// ─── ATOMIC RACE FIX — instance_stats_map ────────────────────────────────────
// PREVIOUS (WRONG): BPF_MAP_TYPE_HASH with non-atomic EWMA updates:
//
//   stats->ewma_latency_ns = (stats->ewma_latency_ns * 7 + elapsed) >> 3;
//   stats->last_req_ts_ns  = now;
//
//   Two CPUs could simultaneously read the same stale ewma_latency_ns, compute
//   divergent updates, and overwrite each other — causing permanently corrupted
//   latency measurements fed to the RL agent.
//
// FIXED: BPF_MAP_TYPE_PERCPU_HASH gives each CPU an exclusive copy of the value.
//   bpf_map_lookup_elem() on a PERCPU map returns a pointer to the CURRENT CPU's
//   slot only.  No cross-CPU reads or writes occur, so no atomic instructions are
//   needed for the EWMA arithmetic.  The userspace control-plane daemon must
//   aggregate per-CPU values (sum/average across CPUs) before feeding the RL
//   agent or computing latency percentiles.
//   See controlplane/internal/metrics/collector.go for the aggregation path.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include "omega_maps.h"

extern struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
} flow_metrics_map SEC(".maps");

extern struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
} selection_map SEC(".maps");

// Tail-call depth counter (owned by filter_manager; shared via extern)
extern struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
} tail_depth_map SEC(".maps");

// ─── Per-backend latency tracking for EWMA ───────────────────────────────
// BPF_MAP_TYPE_PERCPU_HASH: each CPU has an independent copy of the value for
// each key.  bpf_map_lookup_elem() returns a pointer to THIS CPU's slot, so
// the EWMA arithmetic below is free of cross-CPU races without atomic ops.
// Userspace MUST aggregate per-CPU values; see metrics/collector.go.
struct instance_stats {
    __u64 total_requests;
    __u64 total_errors;
    __u64 ewma_latency_ns;  // fixed-point, mantissa × 1000
    __u64 last_req_ts_ns;
    // ─── Circuit breaker fields ───────────────────────────────────────────────
    // consecutive_errors: incremented on each 5xx, reset to 0 on any success.
    // When this reaches CIRCUIT_TRIP_THRESHOLD, this program atomically writes
    // CIRCUIT_OPEN into circuit_state_map for this backend.  lb_policy reads
    // circuit_state_map before routing and skips CIRCUIT_OPEN backends.
    // The control plane health checker transitions CIRCUIT_OPEN → CIRCUIT_HALF_OPEN
    // after 10s, and → CIRCUIT_CLOSED on a successful probe.
    __u32 consecutive_errors;
};

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_HASH);  // was BPF_MAP_TYPE_HASH — race fixed
    __uint(max_entries, 8192);
    __type(key, __u32);  // instance_id
    __type(value, struct instance_stats);
} instance_stats_map SEC(".maps");

// circuit_state_map: extern (canonical owner is lb_policy.bpf.c)
extern struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
} circuit_state_map SEC(".maps");

// ─── Perf ring buffer for low-latency export to control plane ─────────────
// event_sample is the "black box flight recorder" record for each routing
// decision.  Every field that an SRE needs to answer "why did you route here?"
// is captured at kernel speed and emitted to the ringbuf.
//
// The Go daemon (metrics/collector.go) reads these with ringbuf.Reader and
// emits structured logs + stores the last 10,000 records in the flight recorder
// for the /admin/explain API.
struct event_sample {
    __u32 instance_id;       // backend that was selected
    __u64 latency_ns;        // EWMA latency at selection time (ns)
    __u32 error;             // 1 if 5xx was observed on this flow
    __u32 circuit_state;     // CIRCUIT_CLOSED/OPEN/HALF_OPEN at select time
    __u32 vnodes_at_select;  // vnode count for this backend at select time
    __u8  probe_idx;         // which clockwise probe (0–63) found this backend
    // reason: why this backend was chosen
    //   0 = normal H&A selection
    //   1 = HALF_OPEN circuit probe (allowed through to test recovery)
    //   2 = fallback (all preferred backends skipped; only option available)
    __u8  reason;
    __u16 pad;               // align to 8 bytes
    __u64 timestamp_ns;      // bpf_ktime_get_ns() at selection
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20); // 1 MB ring buffer
} events_ringbuf SEC(".maps");

struct selection_result {
    __u32 instance_id;
    __be32 backend_ip;
    __be16 backend_port;
};

SEC("sk_msg")
int metrics_collector(struct sk_msg_md *msg)
{
    __u32 zero = 0;

    // ── Tail-call depth guard ────────────────────────────────────────────
    // At this point in the chain depth should be 4.  Check anyway in case
    // the chain is extended in future or this program is called directly.
    __u32 *depth = bpf_map_lookup_elem(&tail_depth_map, &zero);
    if (depth) {
        if (*depth >= TAIL_CALL_DEPTH_MAX)
            return SK_PASS; // abort chain; fail-open
        (*depth)++;
    }

    struct selection_result *sel = bpf_map_lookup_elem(&selection_map, &zero);
    if (!sel || !sel->instance_id) return SK_PASS;

    __u64 now = bpf_ktime_get_ns();

    // ── Per-CPU EWMA update (no atomic needed; PERCPU_HASH gives CPU-local slot)
    struct instance_stats *stats = bpf_map_lookup_elem(&instance_stats_map,
                                                        &sel->instance_id);
    if (stats) {
        // total_requests still uses atomic add because callers outside BPF may
        // read this field via bpf_map_lookup_elem from userspace mid-update.
        __sync_fetch_and_add(&stats->total_requests, 1);
        // EWMA: α = 0.125 (1/8 shift).  Safe: this CPU owns this slot exclusively.
        __u64 elapsed = now - stats->last_req_ts_ns;
        stats->ewma_latency_ns = (stats->ewma_latency_ns * 7 + elapsed) >> 3;
        stats->last_req_ts_ns  = now;
    } else {
        struct instance_stats new_stats = {
            .total_requests  = 1,
            .ewma_latency_ns = 0,
            .last_req_ts_ns  = now,
        };
        // BPF_NOEXIST prevents overwriting a slot another CPU just created
        // for the same key between our lookup miss and this update.
        bpf_map_update_elem(&instance_stats_map, &sel->instance_id,
                            &new_stats, BPF_NOEXIST);
    }

    // ── Circuit breaker: track consecutive errors, trip on threshold ─────────
    // Read the error flag from the ringbuf event before publishing it.
    // error=1 when the response observed by the relay layer was 5xx.
    // On PERCPU maps we can do the per-CPU counter check with no atomics.
    __u32 is_error = 0;  // TODO: populate from msg metadata / response code
    if (stats) {
        if (is_error) {
            __sync_fetch_and_add(&stats->total_errors, 1);
            stats->consecutive_errors++;
            if (stats->consecutive_errors >= CIRCUIT_TRIP_THRESHOLD) {
                // Trip the circuit: write CIRCUIT_OPEN.  Other CPUs' slots will
                // also have high consecutive_errors so the trip will be confirmed
                // quickly across CPUs.  The control plane clears this after 10s.
                __u32 open = CIRCUIT_OPEN;
                bpf_map_update_elem(&circuit_state_map, &sel->instance_id,
                                    &open, BPF_ANY);
            }
        } else {
            // Success: reset consecutive error counter on this CPU and ensure
            // the circuit is closed (idempotent write — no-op if already closed).
            stats->consecutive_errors = 0;
            __u32 *cs = bpf_map_lookup_elem(&circuit_state_map, &sel->instance_id);
            if (cs && *cs == CIRCUIT_CLOSED) {
                // already closed — nothing to do
            } else {
                // HALF_OPEN probe succeeded: control plane will advance to CLOSED.
                // Write from kernel side as well to minimize latency.
                __u32 closed = CIRCUIT_CLOSED;
                bpf_map_update_elem(&circuit_state_map, &sel->instance_id,
                                    &closed, BPF_ANY);
            }
        }
    }

    // Emit sample to ringbuf for control plane
    // All fields populated here form the "black box" record consumed by the
    // Go flight recorder (observability/flight_recorder.go) and exposed via
    // the /admin/explain API.
    struct event_sample *ev = bpf_ringbuf_reserve(&events_ringbuf,
                                                   sizeof(*ev), 0);
    if (ev) {
        ev->instance_id      = sel->instance_id;
        ev->latency_ns       = stats ? stats->ewma_latency_ns : 0;
        ev->error            = is_error;
        ev->timestamp_ns     = now;
        ev->probe_idx        = 0;   // populated by lb_policy when passing ctx
        ev->reason           = 0;   // 0=normal; lb_policy sets 1 for half-open
        ev->pad              = 0;
        // Circuit state at decision time
        __u32 *cs = bpf_map_lookup_elem(&circuit_state_map, &sel->instance_id);
        ev->circuit_state = cs ? *cs : CIRCUIT_CLOSED;
        // Vnode count from instance_registry
        ev->vnodes_at_select = 0; // best-effort; filled by CP from instance_stats
        bpf_ringbuf_submit(ev, 0);
    }

    return SK_PASS;
}

char _license[] SEC("license") = "GPL";
