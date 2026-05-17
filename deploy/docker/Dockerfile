FROM ubuntu:22.04 AS ebpf-builder

RUN apt-get update && apt-get install -y \
    clang-14 llvm-14 libbpf-dev linux-headers-generic \
    make pkg-config && \
    ln -s /usr/bin/clang-14 /usr/bin/clang

WORKDIR /src/ebpf
COPY ebpf/ .

RUN make -C kern all

# ─── Go builder ────────────────────────────────────────────────────────────
FROM golang:1.22-bookworm AS go-builder

WORKDIR /src
COPY controlplane/ ./controlplane/
COPY --from=ebpf-builder /src/ebpf/kern/*.bpf.o /usr/lib/omega-lb/bpf/

WORKDIR /src/controlplane
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /usr/bin/omegalb ./cmd/omegalb

# ─── Final image ───────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    libbpf1 iproute2 ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=go-builder /usr/bin/omegalb          /usr/bin/omegalb
COPY --from=ebpf-builder /src/ebpf/kern/*.bpf.o /usr/lib/omega-lb/bpf/

RUN useradd --system --no-create-home omega-lb
# Note: the binary itself runs as root inside the container (required for eBPF),
# but the container's UID is omega-lb for audit purposes.

EXPOSE 18000 9090
ENTRYPOINT ["/usr/bin/omegalb"]
