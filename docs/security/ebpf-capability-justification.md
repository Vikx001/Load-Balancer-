# Omega-LB eBPF Capability Security Review

**Document version:** 1.0  
**Status:** For security review  
**Capabilities requested:** `CAP_BPF`, `CAP_NET_ADMIN`, `CAP_SYS_ADMIN`  
**Manifest:** `deploy/kubernetes/daemonset-restricted.yaml`

---

## Executive Summary

Omega-LB uses eBPF sock_ops programs to implement kernel-level load balancing.
This requires three Linux capabilities.  This document explains exactly what each
capability is used for, what it does NOT do, and how each risk is mitigated.

The alternative (no eBPF) is available in `daemonset-fallback.yaml` with zero
capabilities, for environments where these capabilities cannot be granted.

---

## Capability Breakdown

### CAP_BPF

**What we use it for:**  
Calls `bpf(BPF_PROG_LOAD, ...)` to load 5 eBPF programs into the kernel:
`filter_manager`, `route_manager`, `lb_policy`, `connection_relay`, `metrics_collector`.

Calls `bpf(BPF_MAP_CREATE, ...)` to create 7 named maps listed in `omega_maps.h`:
`ha_ring_map`, `ring_meta_map`, `maglev_table_map`, `instance_registry`,
`instance_stats_map`, `events_ringbuf`, `circuit_state_map`.

**What we do NOT use it for:**  
- We do not use `bpf(BPF_BTF_LOAD)` for arbitrary kernel type introspection.
- We do not use `BPF_PROG_TYPE_KPROBE` or `BPF_PROG_TYPE_TRACEPOINT` which
  can observe arbitrary kernel function arguments.
- We do not use `BPF_PROG_TYPE_RAW_TRACEPOINT` or `BPF_PROG_TYPE_LSM`.

**Threat model:**  
An attacker who controls the eBPF programs loaded by this process could read
arbitrary kernel memory.  Mitigation: all programs are compiled from audited
source at `ebpf/kern/`, distributed as part of the container image, and verified
by the kernel eBPF verifier at load time.  The verifier enforces memory safety
and bounds checking before any program executes.

**Kernel verification:** The eBPF verifier runs at load time and rejects programs
that could access out-of-bounds memory, cause unbounded loops, or use disallowed
helper functions.  This is enforced by the kernel regardless of the process's
capabilities.

---

### CAP_NET_ADMIN

**What we use it for:**  
Calls `bpf_prog_attach(BPF_CGROUP_SOCK_OPS)` to attach the `filter_manager`
program to the cgroup v2 hierarchy at the path configured in `ebpf.cgroup_path`
(default: `/sys/fs/cgroup`).  This intercepts new TCP connections created within
that cgroup for load balancing.

**What we do NOT use it for:**  
- We do not call `setsockopt(IP_ADD_MEMBERSHIP)` or modify multicast groups.
- We do not create raw sockets (`SOCK_RAW`).
- We do not modify routing tables (`RTNETLINK`).
- We do not modify iptables, nftables, or tc (traffic control) rules.
- We do not create or modify network interfaces.

**Verification:**  
`grep -r "CAP_NET_ADMIN\|RTNETLINK\|iptables\|ip_add_membership" controlplane/`
should return no results outside of this document.

---

### CAP_SYS_ADMIN

**What we use it for:**  
Pins eBPF maps to `/sys/fs/bpf/omega-lb/` via `bpf(BPF_OBJ_PIN)`.  Map pinning
allows the daemon to restart (systemd `Restart=always`) without losing ring state
— eBPF maps survive the daemon restart and are reused.

**What we do NOT use it for:**  
- We do not call `mount()`, `umount()`, `pivot_root()`, or `chroot()`.
- We do not modify kernel parameters via `/proc/sys`.
- We do not use `keyctl()` or `KEYCTL_CHOWN`.
- We do not use `perf_event_open()` for system-wide profiling.

**Elimination path (kernel ≥ 5.8):**  
On kernel 5.8+, `CAP_BPF` alone is sufficient for `BPF_OBJ_PIN`.  To drop
`CAP_SYS_ADMIN` on a 5.8+ cluster:
1. Set `ebpf.pin_path: ""` in the config (disables map pinning).
2. Remove `SYS_ADMIN` from the capabilities `add` list in the manifest.
3. Accept the trade-off: daemon restart re-creates all maps from scratch
   (takes ~2s; no traffic disruption since NGINX/fallback serves during this window).

---

## What eBPF Programs Do

### filter_manager (program 0, sock_ops hook)

Intercepts new TCP connections via `BPF_SOCK_OPS_ACTIVE_ESTABLISHED_CB`.
Reads protocol, source IP, port from `struct bpf_sock_ops`.
Writes to `scratch_map` (per-CPU, offset 0) with protocol and service_id.
Tail-calls `route_manager`.

**Does NOT:** read packet payload, access other processes' memory, modify DNS,
modify system calls.

### route_manager (program 1)

Reads `request_ctx` from `scratch_map`.
Looks up route rules from `service_config_map` (populated by the Go daemon).
Matches URL path prefix (first 8 bytes) or SNI hostname against rules.
Sets `cluster_id` in `request_ctx`.
Tail-calls `lb_policy`.

**Does NOT:** write to arbitrary kernel memory, access files, network sockets,
or kernel data structures other than the named maps.

### lb_policy (program 2)

Reads the Maglev lookup table and H&A ring to select a backend instance.
Checks circuit breaker state from `circuit_state_map`.
Implements bounded-load probe walk (64 probes max).
Writes selected backend IP/port to `selection_map`.
Tail-calls `connection_relay`.

**Does NOT:** make outbound connections, modify kernel routing, or access
any map not listed in `omega_maps.h`.

### connection_relay (program 3)

Reads selected backend from `selection_map`.
Calls `bpf_sock_ops_cb_flags_set` to redirect the connection to the backend.

**Does NOT:** inspect payload, read TLS keys, or access kernel memory outside
of the named maps.

### metrics_collector (program 4)

Reads latency, error, and circuit state from maps.
Writes `struct event_sample` to `events_ringbuf` for userspace consumption.
Implements circuit breaker trip logic (5 consecutive 5xx → set CIRCUIT_OPEN).

**Does NOT:** exfiltrate data, write to disk, or make system calls.

---

## Auditing the Programs

All eBPF source code is in `ebpf/kern/` and is compiled from source in the
`Dockerfile`.  The binary `.bpf.o` files in the image are reproducible from
the source.

To audit what helper functions each program uses:
```bash
# List all bpf helper calls in the compiled objects
for obj in ebpf/kern/*.bpf.o; do
  echo "=== $obj ==="
  llvm-objdump -d "$obj" | grep -o 'call [0-9]*' | sort | uniq
done

# Verify the programs only access the maps they declare
bpftool prog show   # after daemon starts
bpftool map show    # list all maps created by omega-lb
```

---

## Risk Summary

| Capability | Risk Level | Justification |
|---|---|---|
| `CAP_BPF` | Medium | Programs are audited; kernel verifier enforces memory safety |
| `CAP_NET_ADMIN` | Low | Only used for cgroup sock_ops attach; no route/iptables changes |
| `CAP_SYS_ADMIN` | Low | Only used for bpffs pin; eliminatable on kernel ≥ 5.8 |
| `hostNetwork: true` | Low-Medium | Required for sock_ops on host network namespace |
| `privileged: false` | N/A | Explicitly NOT privileged; uses capability allowlist |

**Overall risk:** Comparable to Cilium, Falco, Calico, and any other eBPF-based
Kubernetes networking or security tool.  All of these use the same three capabilities.

---

## References

- [Cilium capabilities reference](https://docs.cilium.io/en/stable/operations/system_requirements/)
- [Kubernetes CAP_BPF support (KEP-2763)](https://github.com/kubernetes/enhancements/issues/2763)
- [eBPF verifier documentation](https://docs.kernel.org/bpf/verifier.html)
- [Linux capability man page](https://man7.org/linux/man-pages/man7/capabilities.7.html)
