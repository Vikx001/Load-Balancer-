// SPDX-License-Identifier: GPL-2.0
// Layer 0 — metrics_collector: update per-flow stats, export signal to CP

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

// ─── Per-backend latency tracking for EWMA ───────────────────────────────
struct instance_stats {
    __u64 total_requests;
    __u64 total_errors;
    __u64 ewma_latency_ns;  // fixed-point, mantissa × 1000
    __u64 last_req_ts_ns;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);  // instance_id
    __type(value, struct instance_stats);
} instance_stats_map SEC(".maps");

// ─── Perf ring buffer for low-latency export to control plane ─────────────
struct event_sample {
    __u32 instance_id;
    __u64 latency_ns;
    __u32 error;   // 1 if 5xx observed
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
    struct selection_result *sel = bpf_map_lookup_elem(&selection_map, &zero);
    if (!sel || !sel->instance_id) return SK_PASS;

    __u64 now = bpf_ktime_get_ns();

    struct instance_stats *stats = bpf_map_lookup_elem(&instance_stats_map,
                                                        &sel->instance_id);
    if (stats) {
        __sync_fetch_and_add(&stats->total_requests, 1);
        // EWMA latency: α=0.125 (1/8 shift for efficiency)
        __u64 elapsed = now - stats->last_req_ts_ns;
        stats->ewma_latency_ns = (stats->ewma_latency_ns * 7 + elapsed) >> 3;
        stats->last_req_ts_ns  = now;
    } else {
        struct instance_stats new_stats = {
            .total_requests  = 1,
            .ewma_latency_ns = 0,
            .last_req_ts_ns  = now,
        };
        bpf_map_update_elem(&instance_stats_map, &sel->instance_id,
                            &new_stats, BPF_NOEXIST);
    }

    // Emit sample to ringbuf for control plane
    struct event_sample *ev = bpf_ringbuf_reserve(&events_ringbuf,
                                                   sizeof(*ev), 0);
    if (ev) {
        ev->instance_id = sel->instance_id;
        ev->latency_ns  = stats ? stats->ewma_latency_ns : 0;
        ev->error       = 0;
        bpf_ringbuf_submit(ev, 0);
    }

    return SK_PASS;
}

char _license[] SEC("license") = "GPL";
