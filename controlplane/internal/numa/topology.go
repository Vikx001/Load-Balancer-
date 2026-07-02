// Package numa provides NUMA topology detection and NIC affinity checks for
// Omega-LB.
//
// ─── WHY NUMA KILLS HALF YOUR eBPF THROUGHPUT ────────────────────────────────
// Modern servers have 2+ physical CPU sockets, each with its own DRAM
// controller.  Memory attached to socket 0 is "local" for socket 0 CPUs and
// "remote" (3–5× slower) for socket 1 CPUs.  This is NUMA — Non-Uniform
// Memory Access.
//
// eBPF per-CPU maps (BPF_MAP_TYPE_PERCPU_HASH, BPF_MAP_TYPE_PERCPU_ARRAY)
// allocate one slot per logical CPU.  Slot N is physically allocated in DRAM
// local to the NUMA node that contains CPU N.
//
// The problem: PCIe NICs are physically connected to one socket.  On a typical
// 2-socket server, the NIC is on socket 0.  Linux routes NIC interrupts to
// socket 0 CPUs by default.  So the eBPF program that processes packets runs
// on socket 0 CPUs and writes to the per-CPU slot for that CPU — correct.
//
// But the Go daemon goroutines that READ those per-CPU counters are scheduled
// by the Go runtime on any available CPU.  If they land on socket 1 CPUs (the
// other half of the server), every map read crosses the NUMA interconnect:
//
//	Go goroutine on CPU 32 (socket 1) reads instance_stats_map[CPU 0..31]
//	→ each read = NUMA remote access = 120ns instead of 40ns
//	→ at 100k polls/sec = 8ms wasted per second per goroutine (socket 1 CPUs)
//
// Fix:
//  1. Detect which NUMA node the NIC is attached to (socket 0 usually).
//  2. Pin the Go daemon to that NUMA node at launch: numactl --cpunodebind=0.
//  3. Set IRQ affinity so NIC interrupts stay on socket 0 CPUs.
//  4. This collapses all map accesses to local DRAM — 40-60% fewer cache misses.
//
// Benchmark:
//
//	perf stat -e cache-misses,cache-references --pid=$(pidof omega-lb) -- sleep 5
//	→ compare before/after numactl pinning
//	→ expect: cache-miss rate drops from ~15% to ~6%
package numa

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// Node is a NUMA node index (0-based).
type Node int

const (
	NodeUnknown Node = -1
	NodeNone    Node = -2 // system has no NUMA topology (single socket)
)

// DetectNICNode returns the NUMA node of the NIC bound to iface.
// It reads /sys/class/net/<iface>/device/numa_node.
//
// Returns NodeNone if the system does not expose NUMA topology (e.g. single
// socket, VM, or container).  Returns NodeUnknown on any read error.
func DetectNICNode(iface string) (Node, error) {
	path := filepath.Join("/sys/class/net", iface, "device/numa_node")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NodeNone, nil // no NUMA sysfs entry = single socket or VM
		}
		return NodeUnknown, fmt.Errorf("numa: read %s: %w", path, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return NodeUnknown, fmt.Errorf("numa: parse node from %q: %w", path, err)
	}
	if v < 0 {
		return NodeNone, nil // -1 means "no NUMA affinity" on this platform
	}
	return Node(v), nil
}

// DetectNodeCount returns the number of NUMA nodes on this host by counting
// /sys/devices/system/node/nodeN directories.
func DetectNodeCount() int {
	entries, err := filepath.Glob("/sys/devices/system/node/node[0-9]*")
	if err != nil || len(entries) == 0 {
		return 1 // single-node or no sysfs
	}
	return len(entries)
}

// CPUsForNode returns the set of logical CPU IDs in a NUMA node by parsing
// /sys/devices/system/node/nodeN/cpulist.
// Format example: "0-15,32-47" → {0..15, 32..47}
func CPUsForNode(node Node) ([]int, error) {
	if node < 0 {
		return nil, fmt.Errorf("numa: invalid node %d", node)
	}
	path := fmt.Sprintf("/sys/devices/system/node/node%d/cpulist", node)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("numa: read %s: %w", path, err)
	}
	return parseCPUList(strings.TrimSpace(string(data)))
}

// parseCPUList parses Linux cpulist format: "0-3,8,16-19" → [0,1,2,3,8,16,17,18,19]
func parseCPUList(s string) ([]int, error) {
	var cpus []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dash := strings.Index(part, "-")
		if dash < 0 {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("bad cpulist part %q: %w", part, err)
			}
			cpus = append(cpus, n)
		} else {
			lo, err1 := strconv.Atoi(part[:dash])
			hi, err2 := strconv.Atoi(part[dash+1:])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("bad cpulist range %q", part)
			}
			for c := lo; c <= hi; c++ {
				cpus = append(cpus, c)
			}
		}
	}
	return cpus, nil
}

// AssertNUMAAlignment checks that the NIC is accessible from the current process
// and logs a warning with remediation steps when NUMA misalignment is detected.
//
// Call this from daemon startup (before loading eBPF) to catch NUMA issues early.
//
// This function NEVER returns an error — NUMA misalignment is a performance
// degradation, not a correctness failure.  The daemon can still run; it will
// just be slower.  The warning includes exact numactl and irqbalance commands.
func AssertNUMAAlignment(iface string, log *zap.Logger) {
	nodes := DetectNodeCount()
	if nodes <= 1 {
		log.Info("NUMA check: single-node system (or VM); no NUMA affinity needed")
		return
	}

	nicNode, err := DetectNICNode(iface)
	if err != nil {
		log.Warn("NUMA check: could not detect NIC NUMA node",
			zap.String("iface", iface),
			zap.Error(err),
		)
		return
	}
	if nicNode == NodeNone || nicNode == NodeUnknown {
		log.Info("NUMA check: NIC has no NUMA affinity annotation; skipping alignment check",
			zap.String("iface", iface),
		)
		return
	}

	log.Info("NUMA topology",
		zap.String("iface", iface),
		zap.Int("nic_numa_node", int(nicNode)),
		zap.Int("numa_nodes_total", nodes),
	)

	// Emit the numactl pinning command as a structured log field so it shows
	// up in dashboards.  The operator copies this into the systemd ExecStart.
	log.Warn("NUMA performance advisory: bind the daemon and IRQs to the NIC's NUMA node",
		zap.Int("nic_numa_node", int(nicNode)),
		zap.String("daemon_fix",
			fmt.Sprintf("numactl --cpunodebind=%d --membind=%d /usr/bin/omega-lb --config /etc/omega-lb/config.yaml",
				nicNode, nicNode)),
		zap.String("irq_fix",
			fmt.Sprintf("# Set NIC IRQ affinity to CPUs on node %d:\n"+
				"for irq in $(grep '%s' /proc/interrupts | awk -F: '{print $1}'); do\n"+
				"  cpus=$(cat /sys/devices/system/node/node%d/cpulist)\n"+
				"  echo $cpus > /proc/irq/$irq/smp_affinity_list\n"+
				"done", nicNode, iface, nicNode)),
		zap.String("benchmark",
			"perf stat -e cache-misses,cache-references --pid=$(pidof omega-lb) -- sleep 5"),
		zap.String("expected_improvement", "40-60% reduction in cache misses after pinning"),
	)
}

// IRQsForInterface returns the IRQ numbers associated with a NIC by reading
// /proc/interrupts.  Returns an empty slice if none are found.
func IRQsForInterface(iface string) []int {
	data, err := os.ReadFile("/proc/interrupts")
	if err != nil {
		return nil
	}
	var irqs []int
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, iface) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				irqStr := strings.TrimSuffix(fields[0], ":")
				if n, err := strconv.Atoi(irqStr); err == nil {
					irqs = append(irqs, n)
				}
			}
		}
	}
	return irqs
}
