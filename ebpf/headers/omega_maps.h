#pragma once
// omega_maps.h — shared map constants, verifier-safety guards, and operational
// constraints.  Every eBPF source file in this project includes this header.

// ─── Kernel version compat ────────────────────────────────────────────────────
// BPF_SOCK_OPS_TX_SENDMSG_CB was introduced in Linux 6.x (value 17).
// On kernels < 6.x the op value 255 never matches, so filter_manager is a
// transparent pass-through.  The hook activates automatically on 6.x+.
#ifndef BPF_SOCK_OPS_TX_SENDMSG_CB
#define BPF_SOCK_OPS_TX_SENDMSG_CB 255
#endif

// Socket/TCP constants not emitted by bpftool-generated vmlinux.h (they are
// UAPI values, not kernel-internal types).  Values are stable Linux ABI.
#ifndef SOL_SOCKET
#define SOL_SOCKET      1
#endif
#ifndef SO_KEEPALIVE
#define SO_KEEPALIVE    9
#endif
#ifndef IPPROTO_TCP
#define IPPROTO_TCP     6
#endif
#ifndef TCP_KEEPIDLE
#define TCP_KEEPIDLE    4
#endif
#ifndef TCP_KEEPINTVL
#define TCP_KEEPINTVL   5
#endif
#ifndef TCP_KEEPCNT
#define TCP_KEEPCNT     6
#endif
//
// ─── VERIFIER SAFETY NOTES ───────────────────────────────────────────────────
// 1. UNBOUNDED LOOPS → verifier rejects with "back-edge from insn N to M".
//    All loops in this codebase MUST use #pragma unroll with an explicit bound,
//    or bpf_loop() on kernels ≥ 5.17 (-DOMEGALB_KERNEL_517).
//    Path-complexity budget: ≤ 2^20 paths (5.10 limit).  The 64-probe unrolled
//    loop in lb_policy has ≤ 192 verifier paths; the bisect loop ≤ 68.  Both
//    are safe.  Do NOT add branches inside unrolled loops without recalculating.
//
// 2. TAIL-CALL DEPTH → kernel hard limit is 33.  See TAIL_CALL_DEPTH_MAX below.
//
// 3. MAP MEMORY → eBPF maps live in kernel memory (not process heap).
//    See MAP MEMORY BUDGET below.  Startup assertion is in loader.go.
// ─────────────────────────────────────────────────────────────────────────────

#define PROTO_HTTP1  1
#define PROTO_HTTP2  2
#define PROTO_GRPC   3
// PROTO_TLS_SNI: TLS pass-through.  filter_manager extracted the SNI hostname
// from the ClientHello and wrote it into request_ctx.path (up to 64 bytes).
// route_manager matches on hostname instead of URL path.
// No cert required on the LB.  Only cluster-level routing possible.
#define PROTO_TLS_SNI       4
// PROTO_TLS_KTLS: kTLS-terminated connection.  The kernel decrypted the stream
// before handing bytes to eBPF.  request_ctx.path contains plaintext URL bytes.
// Full L7 path-based routing available.  Requires Linux ≥ 4.13 + kTLS cert setup.
#define PROTO_TLS_KTLS      5
// PROTO_TLS_PASSTHROUGH: opaque TLS bytes forwarded to backend unchanged.
// route_manager skips all path-rule matching and uses the default cluster.
#define PROTO_TLS_PASSTHROUGH 6

#define SK_PASS  1
#define SK_DROP  0

#define RING_SIZE          0xFFFFFFFFU   // 2^32 - 1
#define VNODES_PER_SERVER  150

// ─── Tail-call chain depth budget ────────────────────────────────────────────
// Kernel hard limit: 33 total tail calls across an entire chain.
// Core chain: filter_manager(0) → route_manager(1) → lb_policy(2)
//             → connection_relay(3) → metrics_collector(4)  → depth 4 at exit.
// Budget: 10 slots for the core chain; 23 reserved for future extensions.
//
// TAIL_CALL_DEPTH_MAX  — programs MUST NOT tail-call when depth ≥ this value;
//                        return SK_PASS (fail-open) and emit a warning event.
// TAIL_CALL_DEPTH_WARN — emit a ringbuf event when depth reaches this value so
//                        the control plane can alert before the hard limit.
// Tracked per-CPU via tail_depth_map (BPF_MAP_TYPE_PERCPU_ARRAY, key 0).
// filter_manager resets the counter to 0 on every new chain entry.
#define TAIL_CALL_DEPTH_MAX   30
#define TAIL_CALL_DEPTH_WARN  25

// ─── Kernel feature gates ─────────────────────────────────────────────────────
// Three compiled variants; select one per target kernel in the Makefile:
//
//   -DOMEGALB_KERNEL_60   kernel ≥ 6.0  (full: bpf_loop, kptr_xchg, ringbuf
//                                        in socket ops, all sock_ops helpers)
//   -DOMEGALB_KERNEL_517  kernel 5.17–5.x (bpf_loop, no kptr_xchg)
//   -DOMEGALB_KERNEL_515  kernel 5.15–5.16 (default; #pragma unroll only,
//                                           no bpf_loop; minimum supported)
//
// Minimum OS requirement: Ubuntu 22.04 / RHEL 9 / kernel 5.15+.
// Anything older must use the TC-hook fallback (BPF_PROG_TYPE_SCHED_CLS).
// The daemon asserts the running kernel meets the loaded variant at startup.
#if !defined(OMEGALB_KERNEL_60) && \
    !defined(OMEGALB_KERNEL_517) && \
    !defined(OMEGALB_KERNEL_515)
# define OMEGALB_KERNEL_515
#endif

// ─── Map memory budget (kernel-pinned memory, NOT process heap) ───────────────
// Enforced at daemon startup; see controlplane/internal/ebpf/loader.go.
// Approximate footprint on a 32-vCPU node (values kept conservative):
//
//   Map                  Type              Entries  Value   Per-CPU   Total
//   ─────────────────── ─────────────────  ───────  ──────  ───────   ──────
//   ha_ring_map          HASH               65 536    8 B    no        512 KB
//   ring_meta_map        ARRAY                   1  262 KB   no        262 KB
//   maglev_table_map     ARRAY              65 537    4 B    no        256 KB  ← NEW
//   instance_registry    HASH                8 192   28 B    no        224 KB
//   instance_stats_map   PERCPU_HASH         8 192   32 B    yes      ~8 MB
//   flow_metrics_map     LRU_HASH           65 536   40 B    no       2.5 MB
//   events_ringbuf       RINGBUF                 —    —      no         1 MB
//   circuit_state_map    HASH                8 192    4 B    no         32 KB
//   ──────────────────────────────────────────────────────────────────────────
//   Total (32-CPU node)                                             ~12.79 MB
//
// Startup assertion: total_expected_bytes < (MemTotal / 20)  [≡ < 5% of RAM].
// Reduce max_entries or add RAM if the assertion fires.

// ─── Circuit breaker state constants ─────────────────────────────────────────
// Kernel-side circuit breaker achieves ~50ms failure detection vs 6s for the
// health checker.  See metrics_collector.bpf.c for the trip logic and
// lb_policy.bpf.c for the routing skip.
//
// State machine:
//   CLOSED    → normal routing
//   OPEN      → lb_policy skips this backend; control plane sets HALF_OPEN after 10s
//   HALF_OPEN → lb_policy allows exactly 1 probe request; control plane sets
//               CLOSED on probe success, OPEN again on probe failure
//
// Trip condition: CIRCUIT_TRIP_THRESHOLD consecutive 5xx responses.
// Map: circuit_state_map — BPF_MAP_TYPE_HASH, key=instance_id (__u32),
//      value=circuit state (__u32).  Canonical definition in lb_policy.bpf.c.
#define CIRCUIT_CLOSED            0
#define CIRCUIT_OPEN              1
#define CIRCUIT_HALF_OPEN         2
#define CIRCUIT_TRIP_THRESHOLD    5   // consecutive errors before opening circuit

// ─── Maglev hash table — O(1) backend lookup ─────────────────────────────────
//
// WHY: Binary search on a 7,500-entry sorted ring is O(log n) = ~13 comparisons.
// At 1M req/s = 13M comparisons/s.  The 60KB ring doesn't fit in L1 (32KB) so
// every binary search step is a potential cache miss.
//
// FIX: Maglev lookup table (Google, NSDI 2016).  M=65537 slots; lookup is one
// array index: backend = maglev_table[hash % MAGLEV_M].
//
// Memory: 65537 × 4 bytes = 256KB — fits entirely in L2 cache.
// After warm-up, lookup latency is ~1ns vs ~80ns for cold binary search.
//
// The Go control plane (ring/maglev.go) recomputes the table after every vnode
// change via ring.Manager.RebuildMaglevTable() and writes the result here.
// The bisect_right / ring_meta fallback in lb_policy is retained for the
// bounded-load secondary selection only.
#define MAGLEV_M  65537   // must be prime; 65537 is standard Maglev choice
