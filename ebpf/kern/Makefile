CLANG     ?= clang
CFLAGS    := -O2 -g -target bpf \
             -I../headers \
             -D__TARGET_ARCH_x86 \
             -fno-stack-protector

BPF_SRCS  := filter_manager.bpf.c \
             route_manager.bpf.c \
             lb_policy.bpf.c \
             connection_relay.bpf.c \
             metrics_collector.bpf.c

BPF_OBJS  := $(BPF_SRCS:.bpf.c=.bpf.o)

.PHONY: all clean

all: $(BPF_OBJS)

%.bpf.o: %.bpf.c ../headers/vmlinux.h
	$(CLANG) $(CFLAGS) -c $< -o $@

# Generate vmlinux.h from running kernel (requires bpftool)
../headers/vmlinux.h:
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@

clean:
	rm -f $(BPF_OBJS)
