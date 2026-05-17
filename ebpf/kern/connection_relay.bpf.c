// SPDX-License-Identifier: GPL-2.0
// Layer 0 — connection_relay: zero-copy queue splice from p-sock to i-sock
// Uses bpf_msg_redirect_hash to redirect message to pre-warmed i-sock.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include "omega_maps.h"

// ─── i-sock pool: instance_id → sockmap entry ────────────────────────────
// BPF_MAP_TYPE_SOCKHASH allows redirecting sk_msg to a socket by key.
struct {
    __uint(type, BPF_MAP_TYPE_SOCKHASH);
    __uint(max_entries, 8192);
    __type(key, __u32);   // instance_id
    __type(value, __u64); // sock cookie (set at i-sock creation)
} isock_pool SEC(".maps");

// ─── Selection result (from lb_policy) ────────────────────────────────────
struct selection_result {
    __u32 instance_id;
    __be32 backend_ip;
    __be16 backend_port;
};

extern struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
} selection_map SEC(".maps");

extern struct {
    __uint(type, BPF_MAP_TYPE_PROG_ARRAY);
    __uint(max_entries, 8);
} prog_array SEC(".maps");

// ─── HTTP/2 multiplexed stream tracking ───────────────────────────────────
// Maps (instance_id, stream_id) → p-sock cookie for response routing
struct stream_key {
    __u32 instance_id;
    __u32 stream_id;
};

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 32768);
    __type(key, struct stream_key);
    __type(value, __u64); // p-sock cookie
} stream_map SEC(".maps");

#define PROG_METRICS 4

SEC("sk_msg")
int connection_relay(struct sk_msg_md *msg)
{
    __u32 zero = 0;
    struct selection_result *sel = bpf_map_lookup_elem(&selection_map, &zero);
    if (!sel || !sel->instance_id)
        return SK_PASS;

    // Zero-copy redirect: splice TX queue of p-sock into i-sock TX queue.
    // bpf_msg_redirect_hash looks up isock_pool[instance_id] and splices.
    long ret = bpf_msg_redirect_hash(msg, &isock_pool, &sel->instance_id,
                                     BPF_F_INGRESS);
    if (ret < 0)
        return SK_PASS; // i-sock not available, pass to userspace

    bpf_tail_call(msg, &prog_array, PROG_METRICS);
    return SK_PASS;
}

// ─── Response routing for HTTP/2 ─────────────────────────────────────────
// Called on i-sock receive; routes response back to originating p-sock.
SEC("sk_skb/stream_verdict")
int response_router(struct __sk_buff *skb)
{
    // In a full implementation: parse stream_id from HTTP/2 frame header,
    // look up stream_map[(instance_id, stream_id)] to find p-sock cookie,
    // then redirect via bpf_sk_redirect_hash.
    // Simplified: pass through (HTTP/1.1 case handles naturally via i-sock).
    return SK_PASS;
}

char _license[] SEC("license") = "GPL";
