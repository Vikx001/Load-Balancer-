// Package ebpf provides safe loading, hot-reloading, and pre-flight validation
// for all Omega-LB eBPF programs.
//
// Every public function in this package corresponds to a documented failure mode
// in the eBPF layer.  The package must be called from daemon.New() before any
// eBPF program is loaded.
package ebpf

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/numa"
)

// ─── Failure mode: cgroup v1 vs v2 — programs silently do nothing ────────────
//
// BPF_PROG_TYPE_SOCK_OPS programs must be attached to the cgroup v2 unified
// hierarchy.  On cgroup v1 hosts the attach call succeeds but the program is
// attached to the wrong hierarchy and never fires — traffic bypasses the LB
// entirely with no errors logged.
//
// Fix: detect at startup and fail fast if the host cannot support sock_ops.
// Callers must fall back to the TC-hook variant on v1 hosts.

// CgroupVersion is the detected cgroup hierarchy version on the host.
type CgroupVersion int

const (
	CgroupUnknown CgroupVersion = 0
	CgroupV1      CgroupVersion = 1
	CgroupV2      CgroupVersion = 2
)

// DetectCgroupVersion returns the cgroup hierarchy version active on this host.
//
// Detection logic:
//   - cgroup v2 (unified):  /sys/fs/cgroup/cgroup.controllers exists
//   - cgroup v1 (legacy):   /sys/fs/cgroup/cpu exists, but cgroup.controllers absent
//
// An error is returned only when the filesystem is not readable; a missing
// cgroup mount is returned as CgroupUnknown with a descriptive error.
func DetectCgroupVersion() (CgroupVersion, error) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return CgroupV2, nil
	} else if !os.IsNotExist(err) {
		return CgroupUnknown, fmt.Errorf("cgroup v2 detection: %w", err)
	}

	if _, err := os.Stat("/sys/fs/cgroup/cpu"); err == nil {
		return CgroupV1, nil
	} else if !os.IsNotExist(err) {
		return CgroupUnknown, fmt.Errorf("cgroup v1 detection: %w", err)
	}

	return CgroupUnknown, fmt.Errorf("cgroup detection: neither v1 (/sys/fs/cgroup/cpu) " +
		"nor v2 (/sys/fs/cgroup/cgroup.controllers) hierarchy found")
}

// AssertCgroupV2 returns an error if the host does not have cgroup v2, with
// actionable guidance on what to deploy instead.
func AssertCgroupV2(log *zap.Logger) error {
	ver, err := DetectCgroupVersion()
	if err != nil {
		return fmt.Errorf("startup check: %w", err)
	}
	log.Info("cgroup version detected", zap.Int("version", int(ver)))
	if ver != CgroupV2 {
		return fmt.Errorf(
			"startup check: host uses cgroup v%d; BPF_PROG_TYPE_SOCK_OPS requires "+
				"cgroup v2 (unified hierarchy). "+
				"On cgroup v1 hosts load the TC-hook variant (KERNEL_VARIANT=tc) "+
				"and attach via BPF_PROG_TYPE_SCHED_CLS instead. "+
				"Minimum OS: Ubuntu 22.04, RHEL 9, or kernel ≥ 5.15 with "+
				"'systemd.unified_cgroup_hierarchy=1' in the kernel cmdline", int(ver))
	}
	return nil
}

// ─── Failure mode: kernel version fragmentation ───────────────────────────────
//
// eBPF helper functions and map types differ per kernel point release.
// A program that loads on kernel 6.x will fail verification on 5.10.
// The daemon must detect the running kernel version, select the correct
// pre-compiled variant, and refuse to load a variant for a newer kernel.

// KernelVersion holds a parsed kernel release number (major.minor.patch).
type KernelVersion struct {
	Major int
	Minor int
	Patch int
}

// String returns the version as "major.minor.patch".
func (kv KernelVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", kv.Major, kv.Minor, kv.Patch)
}

// AtLeast returns true if kv ≥ other.
func (kv KernelVersion) AtLeast(major, minor int) bool {
	if kv.Major != major {
		return kv.Major > major
	}
	return kv.Minor >= minor
}

// ParseKernelVersion reads /proc/sys/kernel/osrelease and parses the version
// triple.  Extra suffixes ("-generic", "-amd64", etc.) are ignored.
func ParseKernelVersion() (KernelVersion, error) {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return KernelVersion{}, fmt.Errorf("read kernel version: %w", err)
	}
	raw := strings.TrimSpace(string(data))
	// Strip distro suffix after first non-version character
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '-' || r == '+' || r == '~'
	})
	if len(parts) == 0 {
		return KernelVersion{}, fmt.Errorf("parse kernel version: empty osrelease %q", raw)
	}
	nums := strings.SplitN(parts[0], ".", 3)
	if len(nums) < 2 {
		return KernelVersion{}, fmt.Errorf("parse kernel version: too few components in %q", parts[0])
	}
	major, err := strconv.Atoi(nums[0])
	if err != nil {
		return KernelVersion{}, fmt.Errorf("parse kernel major from %q: %w", raw, err)
	}
	minor, err := strconv.Atoi(nums[1])
	if err != nil {
		return KernelVersion{}, fmt.Errorf("parse kernel minor from %q: %w", raw, err)
	}
	patch := 0
	if len(nums) == 3 {
		patch, _ = strconv.Atoi(nums[2]) // best-effort; patch is rarely significant
	}
	return KernelVersion{Major: major, Minor: minor, Patch: patch}, nil
}

// CompatVariant returns the object-file variant suffix to load for the given
// kernel.  Variants correspond to Makefile KERNEL_VARIANT values:
//
//	"60"  — kernel ≥ 6.0 (bpf_loop, kptr_xchg, ringbuf in sock_ops)
//	"517" — kernel 5.17–5.x (bpf_loop, no kptr_xchg)
//	"515" — kernel 5.15–5.16 (#pragma unroll only; default minimum)
//	"tc"  — kernel < 5.15 (TC/sched_cls fallback; sock_ops not supported)
func CompatVariant(kv KernelVersion) string {
	switch {
	case kv.AtLeast(6, 0):
		return "60"
	case kv.AtLeast(5, 17):
		return "517"
	case kv.AtLeast(5, 15):
		return "515"
	default:
		return "tc"
	}
}

// AssertKernelVersion checks that the running kernel meets the minimum
// requirement and logs the selected compatibility variant.
func AssertKernelVersion(log *zap.Logger) (string, error) {
	kv, err := ParseKernelVersion()
	if err != nil {
		return "", fmt.Errorf("startup check: %w", err)
	}
	variant := CompatVariant(kv)
	log.Info("kernel version check",
		zap.String("kernel", kv.String()),
		zap.String("ebpf_variant", variant),
	)
	if variant == "tc" {
		return variant, fmt.Errorf(
			"startup check: kernel %s is below the minimum supported version (5.15). "+
				"Upgrade the kernel or use the TC-hook variant. "+
				"The verifier on kernels < 5.15 rejects programs that use map types, "+
				"helpers, or instruction patterns introduced in 5.15", kv.String())
	}
	return variant, nil
}

// ─── Failure mode: eBPF map memory exhausts kernel RAM ───────────────────────
//
// eBPF maps are pinned in kernel memory, not process heap.  Creating maps with
// oversized max_entries can fragment and exhaust kernel memory while the daemon
// process reports healthy.  Nested maps (map-in-map) with stale references are
// never garbage-collected.
//
// Fix: calculate the expected map footprint at startup and assert it is < 5%
// of available RAM before loading any program.

// MapMemoryBudgetBytes returns the expected total kernel map footprint in bytes
// for the given number of CPUs (used for PERCPU map sizing).
//
// Values are conservative upper bounds; adjust if max_entries are changed.
func MapMemoryBudgetBytes(numCPU int) int64 {
	const (
		haRingMapBytes        int64 = 65536 * (4 + 4)    // 512 KB
		ringMetaMapBytes      int64 = 262148              // 256 KB  (1 × ring_meta)
		instanceRegistryBytes int64 = 8192 * 28           // 224 KB
		flowMetricsMapBytes   int64 = 65536 * 40          // 2.5 MB
		eventsRingbufBytes    int64 = 1 << 20             // 1 MB
	)
	// instance_stats_map is BPF_MAP_TYPE_PERCPU_HASH: one value copy per CPU.
	instanceStatsMapBytes := int64(8192) * 32 * int64(numCPU)

	return haRingMapBytes +
		ringMetaMapBytes +
		instanceRegistryBytes +
		flowMetricsMapBytes +
		eventsRingbufBytes +
		instanceStatsMapBytes
}

// AssertMapMemoryBudget reads /proc/meminfo and verifies that the expected map
// footprint is less than 5% of total RAM.  It returns an error with the exact
// byte counts if the assertion fails so the operator can tune max_entries.
func AssertMapMemoryBudget(log *zap.Logger) error {
	numCPU := runtime.NumCPU()
	expectedBytes := MapMemoryBudgetBytes(numCPU)

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return fmt.Errorf("startup check: read /proc/meminfo: %w", err)
	}

	var totalRAMkB int64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				totalRAMkB, _ = strconv.ParseInt(fields[1], 10, 64)
			}
			break
		}
	}
	if totalRAMkB == 0 {
		return fmt.Errorf("startup check: could not parse MemTotal from /proc/meminfo")
	}

	totalRAMBytes := totalRAMkB * 1024
	budgetBytes := totalRAMBytes / 20 // 5%

	log.Info("eBPF map memory budget check",
		zap.Int64("expected_bytes", expectedBytes),
		zap.Int64("budget_bytes_5pct", budgetBytes),
		zap.Int64("total_ram_bytes", totalRAMBytes),
		zap.Int("num_cpu", numCPU),
	)

	if expectedBytes > budgetBytes {
		return fmt.Errorf(
			"startup check: eBPF map footprint (%d MB, %d CPUs) exceeds 5%% of RAM "+
				"(%d MB available, budget %d MB). "+
				"Reduce max_entries in ha_ring_map, flow_metrics_map, or "+
				"instance_stats_map, or provision a node with more RAM",
			expectedBytes>>20, numCPU, totalRAMBytes>>20, budgetBytes>>20)
	}
	return nil
}

// ─── Failure mode: hot-reloading eBPF programs drops in-flight connections ───
//
// Detaching an old program and attaching a new one creates a 10–100 µs window
// with no program attached.  At 100 k req/s this drops ~10 requests per reload.
//
// Fix: use BPF_LINK_UPDATE (link.Link.Update) which atomically replaces the
// program in the kernel with zero gap.  Never detach-then-attach.
// For config-only changes (routing weights, token-bucket rates), update the
// eBPF maps directly — the running program reads them on every packet, so no
// program reload is needed at all.

// AtomicProgSwap atomically replaces the eBPF program on an existing link
// using BPF_LINK_UPDATE.  This is a zero-gap operation: in-flight connections
// are never left without a program attached.
//
// newProg must be a loaded, verified program of the same type as the program
// currently attached to lnk.  The old program is released after the kernel
// confirms the swap.
func AtomicProgSwap(lnk link.Link, newProg *ebpf.Program, log *zap.Logger) error {
	if lnk == nil {
		return fmt.Errorf("AtomicProgSwap: link is nil")
	}
	if newProg == nil {
		return fmt.Errorf("AtomicProgSwap: new program is nil")
	}
	if err := lnk.Update(newProg); err != nil {
		return fmt.Errorf("atomic prog swap (BPF_LINK_UPDATE): %w — "+
			"ensure new program type matches attached type and "+
			"kernel ≥ 5.7 (BPF_LINK_UPDATE introduced in 5.7)", err)
	}
	log.Info("eBPF program atomically swapped via BPF_LINK_UPDATE",
		zap.String("new_prog", newProg.String()),
	)
	return nil
}

// ─── Pre-flight validation entrypoint ────────────────────────────────────────
//
// RunPreflightChecks executes all startup assertions in dependency order.
// It returns the detected eBPF compatibility variant string on success.
// Call this from daemon.New() before loading any eBPF object file.
func RunPreflightChecks(iface string, log *zap.Logger) (variant string, err error) {
	// 1. Kernel version — determines which compiled variant to load.
	variant, err = AssertKernelVersion(log)
	if err != nil {
		return "", err
	}

	// 2. Cgroup hierarchy — sock_ops programs require cgroup v2.
	if err = AssertCgroupV2(log); err != nil {
		return "", err
	}

	// 3. Map memory budget — must pass before any map is created.
	if err = AssertMapMemoryBudget(log); err != nil {
		return "", err
	}

	// 4. NUMA alignment — advisory only; never fatal.
	// Logs the numactl pinning command when the daemon is not bound to the
	// NIC's NUMA node.  On single-socket or VM hosts this is a no-op.
	if iface != "" {
		numa.AssertNUMAAlignment(iface, log)
	}

	log.Info("eBPF pre-flight checks passed",
		zap.String("kernel_variant", variant),
	)
	return variant, nil
}

// ─── Failure mode: daemon crash loses all eBPF state ─────────────────────────
//
// By default eBPF maps live only as long as the file descriptors holding them
// are open.  When the Go daemon crashes, all maps disappear — the next daemon
// restart starts cold with an empty ring, causing a thundering-herd burst on
// every backend simultaneously.
//
// There is a second, subtler problem: the eBPF data plane is attached to the
// cgroup and keeps routing traffic even while the daemon is down (kernel
// programs run independently of user space).  Without pinned maps the programs
// are routing with stale or absent ring data for the entire restart window.
//
// The fix has two parts:
//  1. Pin every map to a stable path in the BPF filesystem (/sys/fs/bpf/omega/)
//     immediately after loading.  Pinned maps survive process death.
//  2. On daemon restart, check whether pinned maps already exist and open them
//     instead of creating new ones.  This preserves ring state accumulated
//     before the crash and avoids a cold-start thundering herd.
//
// Operational commands:
//   # Inspect ring state after a crash:
//   $ bpftool map dump pinned /sys/fs/bpf/omega/ha_ring_map | head -20
//   # Force a clean slate (only after planned maintenance):
//   $ rm -rf /sys/fs/bpf/omega && systemctl restart omega-lb

// PinAllMaps pins every map in coll to pinPath/<map_name>.
// If a pin already exists, it is updated to point to the current map fd;
// stale pins are overwritten.  This must be called immediately after loading
// the eBPF collection so that the maps survive a daemon process restart.
func PinAllMaps(coll *ebpf.Collection, pinPath string, log *zap.Logger) error {
	if err := os.MkdirAll(pinPath, 0700); err != nil {
		return fmt.Errorf("pin maps: mkdir %s: %w", pinPath, err)
	}
	for name, m := range coll.Maps {
		path := pinPath + "/" + name
		if err := m.Pin(path); err != nil {
			return fmt.Errorf("pin map %q → %s: %w", name, path, err)
		}
		log.Info("eBPF map pinned", zap.String("map", name), zap.String("path", path))
	}
	return nil
}

// ReattachStatus describes what PinOrReuse found on startup.
type ReattachStatus int

const (
	// ReattachFreshLoad: no existing pins found; collection was freshly loaded
	// and pins were created.
	ReattachFreshLoad ReattachStatus = iota
	// ReattachReused: existing pinned maps found and reused; no data-plane gap.
	ReattachReused
)

// PinOrReuse is the recommended startup entry point for eBPF loading.
//
// It checks whether the sentinel map (ha_ring_map) is already pinned at
// pinPath.  If yes, it opens all pinned maps as read-write and returns
// ReattachReused — the data plane kept routing during the daemon restart gap
// without any state loss.  If no pins are found (first start or intentional
// wipe), it loads the collection from objPath, pins all maps, and returns
// ReattachFreshLoad.
//
// The caller is responsible for attaching programs to cgroup after loading.
// On ReattachReused, programs are still attached from the previous run — do
// NOT re-attach, as duplicate cgroup attachment causes double-processing.
func PinOrReuse(objPath, pinPath string, log *zap.Logger) (*ebpf.Collection, ReattachStatus, error) {
	sentinel := pinPath + "/ha_ring_map"
	if _, err := os.Stat(sentinel); err == nil {
		// Pinned maps exist — open them.
		log.Info("existing pinned eBPF maps found; re-attaching without data-plane interruption",
			zap.String("pin_path", pinPath),
			zap.String("action", "no cgroup re-attach needed; programs still running"),
		)
		coll, err := openPinnedCollection(pinPath, log)
		if err != nil {
			// Corrupt pins: fall through to fresh load.
			log.Warn("pinned maps unreadable; falling back to fresh load",
				zap.Error(err),
				zap.String("action", "rm -rf "+pinPath+" to clear stale pins"),
			)
		} else {
			return coll, ReattachReused, nil
		}
	}

	// No pins or corrupt pins — load fresh.
	spec, err := ebpf.LoadCollectionSpec(objPath)
	if err != nil {
		return nil, ReattachFreshLoad, fmt.Errorf("load eBPF collection spec from %s: %w", objPath, err)
	}
	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, ReattachFreshLoad, fmt.Errorf("create eBPF collection: %w", err)
	}
	if err := PinAllMaps(coll, pinPath, log); err != nil {
		coll.Close()
		return nil, ReattachFreshLoad, err
	}
	log.Info("eBPF collection freshly loaded and all maps pinned",
		zap.String("obj", objPath),
		zap.String("pin_path", pinPath),
	)
	return coll, ReattachFreshLoad, nil
}

// openPinnedCollection opens the known Omega-LB maps from their pinned paths.
// This is the re-attach path: called when a previous daemon instance pinned the
// maps and we want to reuse them without recreating.
//
// Any map that is missing is skipped (logged at Warn); the caller should decide
// whether missing maps are fatal for its use case.
func openPinnedCollection(pinPath string, log *zap.Logger) (*ebpf.Collection, error) {
	// Known Omega-LB map names (must match SEC(".maps") names in BPF C).
	mapNames := []string{
		"ha_ring_map",
		"instance_registry",
		"isock_pool",
		"instance_stats_map",
		"circuit_state_map",
		"events_ringbuf",
	}
	coll := &ebpf.Collection{
		Maps:     make(map[string]*ebpf.Map),
		Programs: make(map[string]*ebpf.Program),
	}
	for _, name := range mapNames {
		path := pinPath + "/" + name
		m, err := ebpf.LoadPinnedMap(path, nil)
		if err != nil {
			log.Warn("pinned map not found or unreadable",
				zap.String("map", name),
				zap.String("path", path),
				zap.Error(err),
			)
			continue
		}
		coll.Maps[name] = m
		log.Info("pinned map reopened", zap.String("map", name))
	}
	if len(coll.Maps) == 0 {
		return nil, fmt.Errorf("no pinned maps could be opened at %s", pinPath)
	}
	return coll, nil
}

