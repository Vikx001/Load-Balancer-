package numa

import (
	"reflect"
	"testing"

	"go.uber.org/zap"
)

func TestParseCPUListFormats(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"0-3,8,16-19", []int{0, 1, 2, 3, 8, 16, 17, 18, 19}, false},
		{"0-15,32-47", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47}, false},
		{"5", []int{5}, false},
		{"", nil, false},
		{"abc", nil, true},
		{"1-", nil, true},
		{"1-2-3", nil, true},
	}
	for _, c := range cases {
		got, err := parseCPUList(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseCPUList(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCPUList(%q): unexpected error: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseCPUList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDetectNICNodeNonexistentInterfaceReturnsNodeNone(t *testing.T) {
	// No /sys/class/net entry exists for this made-up interface on any
	// platform this test runs on (including macOS, which has no /sys at all) —
	// exercising the "no NUMA sysfs entry" graceful-fallback path.
	node, err := DetectNICNode("omega-test-nonexistent-iface0")
	if err != nil {
		t.Fatalf("expected no error for a missing sysfs path, got %v", err)
	}
	if node != NodeNone {
		t.Fatalf("expected NodeNone for a nonexistent interface, got %v", node)
	}
}

func TestDetectNodeCountFallsBackToOneWithoutNUMASysfs(t *testing.T) {
	// This test environment has no /sys/devices/system/node/nodeN entries
	// (true on macOS, and on most single-socket/VM Linux hosts too).
	if n := DetectNodeCount(); n < 1 {
		t.Fatalf("DetectNodeCount must never return less than 1, got %d", n)
	}
}

func TestCPUsForNodeRejectsNegativeNode(t *testing.T) {
	if _, err := CPUsForNode(Node(-1)); err == nil {
		t.Fatal("expected an error for a negative NUMA node")
	}
	if _, err := CPUsForNode(NodeUnknown); err == nil {
		t.Fatal("expected an error for NodeUnknown")
	}
}

func TestIRQsForInterfaceReturnsNilWithoutProcInterrupts(t *testing.T) {
	// /proc/interrupts does not exist on this platform — must degrade to an
	// empty result, never panic.
	if irqs := IRQsForInterface("eth0"); irqs != nil {
		t.Fatalf("expected nil when /proc/interrupts is unavailable, got %v", irqs)
	}
}

func TestAssertNUMAAlignmentDoesNotPanicOnSingleNodeSystem(t *testing.T) {
	// Smoke test: on a system reporting <=1 NUMA node (true here), this must
	// log and return immediately without touching any NIC sysfs path.
	AssertNUMAAlignment("eth0", zap.NewNop())
}
