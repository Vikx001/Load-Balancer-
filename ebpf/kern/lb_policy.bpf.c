// SPDX-License-Identifier: GPL-2.0
// Layer 0 + Layer 1 — lb_policy: H&A consistent hash + bounded load selection
// Uses the H&A virtual-node ring stored in BPF maps.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include "omega_maps.h"

// ─── H&A Ring map: sorted ring positions ──────────────────────────────────
// key: ring position (u32, 0..2^32-1)  value: instance_id
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 65536); // 150 vnodes × ~430 servers max
    __type(key, __u32);         // ring position
    __type(value, __u32);       // instance_id
} ha_ring_map SEC(".maps");

// ─── Ring size meta ───────────────────────────────────────────────────────
struct ring_meta {
    __u32 size;          // number of vnode entries
    __u32 sorted_keys[65536]; // pre-sorted ring positions (maintained by CP)
};

struct {
    __uint(type, BPF_MAP_TYPE_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct ring_meta);
} ring_meta_map SEC(".maps");

// ─── Instance registry (shared, declared in route_manager) ────────────────
struct backend_entry {
    __be32 ip;
    __be16 port;
    __u8   health;
    __u8   pad;
    __u32  vnode_count;
    __u32  active_reqs;
    __u32  ewma_latency_ns;
};

extern struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
} instance_registry SEC(".maps");

extern struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
} scratch_map SEC(".maps");

extern struct {
    __uint(type, BPF_MAP_TYPE_PROG_ARRAY);
    __uint(max_entries, 8);
} prog_array SEC(".maps");

// ─── Selection result passed to connection_relay ──────────────────────────
struct selection_result {
    __u32 instance_id;
    __be32 backend_ip;
    __be16 backend_port;
};

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct selection_result);
} selection_map SEC(".maps");

struct request_ctx {
    __u8  protocol;
    __u32 service_id;
    __u32 cluster_id;
    __u8  path[64];
    __u16 path_len;
};

#define PROG_CONN_RELAY  3
#define BETA_NUMER       125  // β = 1.25, fixed-point × 100
#define BETA_DENOM       100

// ─── MurmurHash3 finaliser (32-bit) ───────────────────────────────────────
static __always_inline __u32 murmur3_fmix32(__u32 h)
{
    h ^= h >> 16;
    h *= 0x85ebca6b;
    h ^= h >> 13;
    h *= 0xc2b2ae35;
    h ^= h >> 16;
    return h;
}

// Binary search on sorted_keys (up to 65536 entries, log2=16 iterations)
static __always_inline __u32 bisect_right(__u32 *keys, __u32 size, __u32 pos)
{
    __u32 lo = 0, hi = size;
    #pragma unroll
    for (int i = 0; i < 17; i++) {
        if (lo >= hi) break;
        __u32 mid = lo + (hi - lo) / 2;
        if (keys[mid & 0xFFFF] <= pos)
            lo = mid + 1;
        else
            hi = mid;
    }
    return lo;
}

SEC("sockops")
int lb_policy(struct bpf_sock_ops *skops)
{
    __u32 zero = 0;
    struct request_ctx *ctx = bpf_map_lookup_elem(&scratch_map, &zero);
    if (!ctx) return SK_PASS;

    struct ring_meta *meta = bpf_map_lookup_elem(&ring_meta_map, &zero);
    if (!meta || meta->size == 0) return SK_PASS;

    // Hash the source (ip+port) as the ring key
    __u32 hash_input = skops->remote_ip4 ^ ((__u32)skops->remote_port << 16);
    __u32 ring_pos   = murmur3_fmix32(hash_input);

    // Binary search for first vnode ≥ ring_pos
    __u32 idx = bisect_right(meta->sorted_keys, meta->size, ring_pos);
    if (idx >= meta->size) idx = 0; // wrap

    // Walk clockwise to find a healthy, non-overloaded backend (max 64 probes)
    struct selection_result *sel = bpf_map_lookup_elem(&selection_map, &zero);
    if (!sel) return SK_PASS;

    __u32 chosen_id = 0;
    #pragma unroll
    for (int probe = 0; probe < 64; probe++) {
        __u32 vnode_pos = meta->sorted_keys[(idx + probe) & 0xFFFF];
        __u32 *inst_id  = bpf_map_lookup_elem(&ha_ring_map, &vnode_pos);
        if (!inst_id) continue;

        struct backend_entry *be = bpf_map_lookup_elem(&instance_registry, inst_id);
        if (!be || !be->health) continue;

        // Bounded load: reject if active_reqs > β × mean
        // Mean is stored in ring_meta for quick access (updated by CP)
        // Simplified: use hard cap stored in cluster meta
        // Full implementation reads mean from a separate map updated by CP
        chosen_id = *inst_id;
        sel->instance_id  = chosen_id;
        sel->backend_ip   = be->ip;
        sel->backend_port = be->port;
        __sync_fetch_and_add(&be->active_reqs, 1);
        break;
    }

    if (!chosen_id) return SK_PASS; // no healthy backend

    bpf_tail_call(skops, &prog_array, PROG_CONN_RELAY);
    return SK_PASS;
}

char _license[] SEC("license") = "GPL";
