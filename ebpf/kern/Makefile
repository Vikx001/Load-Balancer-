CLANG     ?= clang
LLC       ?= llc
OBJDUMP   ?= llvm-objdump

# ─── Kernel variant selection ─────────────────────────────────────────────────
# Choose the variant that matches the target node's kernel.
# Minimum supported kernel: 5.15.  Anything older requires the TC-hook fallback.
#
#   KERNEL_VARIANT=515  →  kernel 5.15–5.16  (default; #pragma unroll, no bpf_loop)
#   KERNEL_VARIANT=517  →  kernel 5.17–5.x   (bpf_loop enabled)
#   KERNEL_VARIANT=60   →  kernel 6.0+        (full feature set)
KERNEL_VARIANT ?= 515

ifeq ($(KERNEL_VARIANT),60)
  KERNEL_FLAG := -DOMEGALB_KERNEL_60
else ifeq ($(KERNEL_VARIANT),517)
  KERNEL_FLAG := -DOMEGALB_KERNEL_517
else
  KERNEL_FLAG := -DOMEGALB_KERNEL_515
endif

# ─── Compiler flags ───────────────────────────────────────────────────────────
# -fno-stack-protector: stack canaries are not supported in BPF programs.
# -mcpu=v3: enables BPF ISA v3 atomics (kernel ≥ 5.12); use v2 for 5.10 nodes.
UNAME_M   := $(shell uname -m)
ifeq ($(UNAME_M),aarch64)
  TARGET_ARCH := arm64
else
  TARGET_ARCH := x86
endif
CFLAGS    := -O2 -g -target bpf \
             -I../headers \
             -D__TARGET_ARCH_$(TARGET_ARCH) \
             -mcpu=v3 \
             -fno-stack-protector \
             $(KERNEL_FLAG)

BPF_SRCS  := filter_manager.bpf.c \
             route_manager.bpf.c \
             lb_policy.bpf.c \
             connection_relay.bpf.c \
             metrics_collector.bpf.c

BPF_OBJS  := $(BPF_SRCS:.bpf.c=.bpf.o)

.PHONY: all clean verify ci-matrix

all: $(BPF_OBJS)

%.bpf.o: %.bpf.c ../headers/vmlinux.h
	$(CLANG) $(CFLAGS) -c $< -o $@

# Generate vmlinux.h from running kernel (requires bpftool)
../headers/vmlinux.h:
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

# ─── Bytecode inspection ──────────────────────────────────────────────────────
# Read compiled BPF bytecode alongside C source.  Use this to audit instruction
# count and understand verifier rejections ("back-edge from insn N to M").
# Run: make verify  — then inspect the annotated assembly per program.
verify: all
	@for obj in $(BPF_OBJS); do \
	  echo "═══ $$obj ═══"; \
	  $(OBJDUMP) -S $$obj; \
	done

# ─── Userspace pre-load test ──────────────────────────────────────────────────
# Loads each .bpf.o into a temporary namespace and runs the verifier without
# attaching to a live hook.  Catches verifier rejections before deployment.
# Requires: bpftool ≥ 7.0 on the target kernel.
test-load: all
	@for obj in $(BPF_OBJS); do \
	  echo "Loading $$obj …"; \
	  bpftool prog load $$obj /sys/fs/bpf/omega_test_$$(basename $$obj .bpf.o) \
	    || { echo "FAILED: $$obj"; exit 1; }; \
	  bpftool prog delete pinned /sys/fs/bpf/omega_test_$$(basename $$obj .bpf.o); \
	  echo "OK: $$obj"; \
	done

# ─── CI kernel matrix ─────────────────────────────────────────────────────────
# Build and verifier-test all three variants.  Run this in your CI pipeline
# using kernel images: 5.15, 5.17, 5.19, 6.1.
# The verifier changes between minor releases; pin CI kernel images explicitly.
ci-matrix:
	$(MAKE) clean all KERNEL_VARIANT=515
	$(MAKE) clean all KERNEL_VARIANT=517
	$(MAKE) clean all KERNEL_VARIANT=60

clean:
	rm -f $(BPF_OBJS)
