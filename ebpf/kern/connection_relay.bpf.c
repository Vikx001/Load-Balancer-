// SPDX-License-Identifier: GPL-2.0
// Layer 0 — connection_relay: zero-copy queue splice from p-sock to i-sock
// Uses bpf_msg_redirect_hash to redirect message to pre-warmed i-sock.
//
// ─── VERIFIER SAFETY ─────────────────────────────────────────────────────────
// This program contains no loops.  bpf_msg_redirect_hash is a helper, not an
// iteration construct.  The tail-call depth is checked at entry; at this point
// in the chain depth is 3 (filter=0, route=1, lb_policy=2, relay=3).

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

// Tail-call depth counter (owned by filter_manager; shared via extern)
extern struct {
    __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
    __uint(max_entries, 1);
} tail_depth_map SEC(".maps");

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

    // ── Tail-call depth guard ────────────────────────────────────────────
    __u32 *depth = bpf_map_lookup_elem(&tail_depth_map, &zero);
    if (depth) {
        if (*depth >= TAIL_CALL_DEPTH_MAX)
            return SK_PASS; // abort chain; fail-open
        (*depth)++;
    }

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

// ─── TCP keepalive for i-socks ────────────────────────────────────────────────
// i-sock pool failure mode: when a backend restarts, the kernel sends a TCP RST.
// The SOCKHASH entry for that backend still holds the dead socket fd.  The next
// bpf_msg_redirect_hash call silently fails or delivers to the wrong socket.
//
// Fix: set SO_KEEPALIVE on every outgoing connection (i-sock) at the time it is
// established.  Keepalive parameters:
//   TCP_KEEPIDLE  = 5s  — send first keepalive probe after 5s of silence
//   TCP_KEEPINTVL = 2s  — subsequent probes every 2s
//   TCP_KEEPCNT   = 3   — 3 probes before declaring dead
//
// Dead detection window: 5 + 2 + 2 + 2 = 11s worst-case.
// After detection, the kernel sends RST and closes the socket.  The pool monitor
// (controlplane/internal/ring/pool_monitor.go) removes the stale entry from the
// SOCKHASH and forces the control plane to reconnect.
//
// Requires: cgroup/sock_ops attach point on the same cgroup as the i-socks.
// This SEC is separate from the main relay SEC so it can be selectively attached.
SEC("sockops")
int isock_keepalive(struct bpf_sock_ops *skops)
{
    // Only act on new outgoing TCP connections.
    // BPF_SOCK_OPS_ACTIVE_ESTABLISHED_CB fires when the local TCP stack
    // transitions to ESTABLISHED on a connect() call (active open).
    if (skops->op != BPF_SOCK_OPS_ACTIVE_ESTABLISHED_CB)
        return 1;

    // Enable keepalive at socket level
    int val = 1;
    bpf_setsockopt(skops, SOL_SOCKET, SO_KEEPALIVE, &val, sizeof(val));

    // First idle probe after 5s
    val = 5;
    bpf_setsockopt(skops, IPPROTO_TCP, TCP_KEEPIDLE, &val, sizeof(val));

    // Subsequent probes every 2s
    val = 2;
    bpf_setsockopt(skops, IPPROTO_TCP, TCP_KEEPINTVL, &val, sizeof(val));

    // 3 unanswered probes = dead; total window = 5 + 2×3 = 11s
    val = 3;
    bpf_setsockopt(skops, IPPROTO_TCP, TCP_KEEPCNT, &val, sizeof(val));

    return 1;
}

char _license[] SEC("license") = "GPL";
