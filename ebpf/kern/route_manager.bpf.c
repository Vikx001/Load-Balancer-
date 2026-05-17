// SPDX-License-Identifier: GPL-2.0
// Layer 0 — route_manager: path/header matching → cluster selection
// Tail-calls into lb_policy.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include "omega_maps.h"
#include "omega_proto.h"

// External maps declared in filter_manager (shared via object skeleton)
extern struct {
    __uint(type, BPF_MAP_TYPE_PROG_ARRAY);
    __uint(max_entries, 8);
} prog_array SEC(".maps");

extern struct {
    __uint(type, BPF_MAP_TYPE_HASH_OF_MAPS);
    __uint(max_entries, 256);
} service_config_map SEC(".maps");

extern struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
} scratch_map SEC(".maps");

// ─── Inner map value (route rule) ─────────────────────────────────────────
struct route_rule {
    __u8  protocol;
    __u8  path_prefix[64];
    __u32 cluster_id;
    __u32 weight;
};

// ─── Instance registry: cluster_id → array of backend (ip, port) ──────────
struct backend_entry {
    __be32 ip;
    __be16 port;
    __u8   health;     // 1=alive, 0=draining
    __u8   pad;
    __u32  vnode_count; // current virtual nodes (updated by H&A)
    __u32  active_reqs;
    __u32  ewma_latency_ns;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 8192);
    __type(key, __u32);  // instance_id = cluster_id<<16 | backend_idx
    __type(value, struct backend_entry);
} instance_registry SEC(".maps");

// ─── Cluster meta: cluster_id → backend count ─────────────────────────────
struct cluster_meta {
    __u32 backend_count;
    __u32 total_vnodes;
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 256);
    __type(key, __u32);
    __type(value, struct cluster_meta);
} cluster_meta_map SEC(".maps");

// ─── Request context (from scratch_map) ───────────────────────────────────
struct request_ctx {
    __u8  protocol;
    __u32 service_id;
    __u32 cluster_id;
    __u8  path[64];
    __u16 path_len;
};

#define PROG_LB_POLICY 2

static __always_inline int match_prefix(const __u8 *path, __u16 path_len,
                                        const __u8 *prefix)
{
    // Compare up to 8 bytes (eBPF stack constraint; full prefix in practice
    // needs loop unrolling or a helper).
    #pragma unroll
    for (int i = 0; i < 8; i++) {
        if (prefix[i] == 0) return 1; // prefix exhausted → match
        if (i >= path_len)  return 0;
        if (path[i] != prefix[i]) return 0;
    }
    return 1;
}

SEC("sockops")
int route_manager(struct bpf_sock_ops *skops)
{
    __u32 zero = 0;
    struct request_ctx *ctx = bpf_map_lookup_elem(&scratch_map, &zero);
    if (!ctx)
        return SK_PASS;

    // Look up inner route-rule map for this service
    __u32 *inner_fd = bpf_map_lookup_elem(&service_config_map, &ctx->service_id);
    if (!inner_fd) {
        // No config for this service — default cluster 0
        ctx->cluster_id = 0;
        bpf_tail_call(skops, &prog_array, PROG_LB_POLICY);
        return SK_PASS;
    }

    // Iterate rules (up to 32, unrolled) to find first prefix match
    __u32 matched_cluster = 0;
    #pragma unroll
    for (__u32 rule_id = 0; rule_id < 32; rule_id++) {
        struct route_rule *rule = bpf_map_lookup_elem((void *)(long)*inner_fd, &rule_id);
        if (!rule) break;
        if (rule->protocol != ctx->protocol && rule->protocol != 0) continue;
        if (match_prefix(ctx->path, ctx->path_len, rule->path_prefix)) {
            matched_cluster = rule->cluster_id;
            break;
        }
    }

    ctx->cluster_id = matched_cluster;
    bpf_tail_call(skops, &prog_array, PROG_LB_POLICY);
    return SK_PASS;
}

char _license[] SEC("license") = "GPL";
