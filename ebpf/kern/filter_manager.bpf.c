// SPDX-License-Identifier: GPL-2.0
// Layer 0 — filter_manager: L7 protocol parser and rate-limit gate
// Tail-calls into route_manager on success.
// Hooks: BPF_PROG_TYPE_SOCK_OPS / MSG_SENDMSG

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>
#include "omega_maps.h"
#include "omega_proto.h"

// ─── Rate-limit token-bucket map (updated every 100ms by DQN+A3C) ─────────
// key: service_id  value: current_tokens (atomic s64)
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u32);
    __type(value, __s64);
} rate_limit_map SEC(".maps");

// ─── Prog-array for tail calls ─────────────────────────────────────────────
struct {
    __uint(type, BPF_MAP_TYPE_PROG_ARRAY);
    __uint(max_entries, 8);
    __type(key, __u32);
    __type(value, __u32);
} prog_array SEC(".maps");

#define PROG_ROUTE_MANAGER   1
#define PROG_LB_POLICY       2
#define PROG_CONN_RELAY      3
#define PROG_METRICS         4

// ─── Service config map (nested: service_id → route rules) ────────────────
struct route_rule {
    __u8  protocol;       // PROTO_HTTP1, PROTO_HTTP2, PROTO_GRPC
    __u8  path_prefix[64];
    __u32 cluster_id;
    __u32 weight;         // 0..1000 fixed-point
};

struct {
    __uint(type, BPF_MAP_TYPE_HASH_OF_MAPS);
    __uint(max_entries, 256);
    __type(key, __u32);    // service_id
    __type(value, __u32);  // inner map fd
} service_config_map SEC(".maps");

// ─── Per-flow metrics (5-tuple → counters) ────────────────────────────────
struct flow_key {
    __be32 src_ip;
    __be32 dst_ip;
    __be16 src_port;
    __be16 dst_port;
    __u8   proto;
    __u8   pad[3];
};

struct flow_metrics {
    __u64 tx_bytes;
    __u64 rx_bytes;
    __u64 request_count;
    __u64 no_route_matches;
};

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
    __type(key, struct flow_key);
    __type(value, struct flow_metrics);
} flow_metrics_map SEC(".maps");

// ─── Scratch space (per-CPU) for parsed request context ───────────────────
struct request_ctx {
    __u8  protocol;
    __u32 service_id;
    __u32 cluster_id;
    __u8  path[64];
    __u16 path_len;
};

struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
    __type(key, __u32);
    __type(value, struct request_ctx);
} scratch_map SEC(".maps");

// ─── Helpers ──────────────────────────────────────────────────────────────
static __always_inline int consume_token(__u32 service_id)
{
    __s64 *tokens = bpf_map_lookup_elem(&rate_limit_map, &service_id);
    if (!tokens)
        return 1; // no limit configured → allow

    __s64 cur = __sync_fetch_and_sub(tokens, 1);
    if (cur <= 0) {
        __sync_fetch_and_add(tokens, 1); // refund
        return 0; // rate limited
    }
    return 1; // allowed
}

static __always_inline __u8 detect_protocol(struct bpf_sock_ops *skops)
{
    // Heuristic: inspect first bytes of the send buffer via bpf_skb helpers.
    // Full implementation requires reading from skops->skb_data.
    // Return PROTO_HTTP2 if preface "PRI * HTTP/2.0" detected, else HTTP1.
    return PROTO_HTTP1;
}

// ─── Main entry: called on MSG_SENDMSG ────────────────────────────────────
SEC("sockops")
int filter_manager(struct bpf_sock_ops *skops)
{
    if (skops->op != BPF_SOCK_OPS_TX_SENDMSG_CB)
        return SK_PASS;

    __u32 zero = 0;
    struct request_ctx *ctx = bpf_map_lookup_elem(&scratch_map, &zero);
    if (!ctx)
        return SK_PASS;

    // Detect L7 protocol
    ctx->protocol = detect_protocol(skops);

    // Derive service_id from destination port (simplified; real impl uses SNI/host header)
    ctx->service_id = bpf_ntohl(skops->remote_port) & 0xFFFF;

    // Rate-limit check (Layer 4 gate)
    if (!consume_token(ctx->service_id)) {
        // Drop → caller gets EAGAIN (maps to HTTP 429 at p-sock level)
        return SK_DROP;
    }

    // Update flow metrics
    struct flow_key fkey = {
        .src_ip   = skops->local_ip4,
        .dst_ip   = skops->remote_ip4,
        .src_port = (__be16)skops->local_port,
        .dst_port = (__be16)skops->remote_port,
        .proto    = 6, // TCP
    };
    struct flow_metrics *fm = bpf_map_lookup_elem(&flow_metrics_map, &fkey);
    if (fm) {
        __sync_fetch_and_add(&fm->request_count, 1);
    } else {
        struct flow_metrics new_fm = { .request_count = 1 };
        bpf_map_update_elem(&flow_metrics_map, &fkey, &new_fm, BPF_NOEXIST);
    }

    // Hand off to route_manager
    bpf_tail_call(skops, &prog_array, PROG_ROUTE_MANAGER);
    return SK_PASS; // tail call failed — allow through (fail-open)
}

char _license[] SEC("license") = "GPL";
