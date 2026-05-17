#!/usr/bin/env bash
# HTTP load benchmark using wrk2 (https://github.com/giltene/wrk2)
# Run AFTER omega-lb is serving on localhost:80

set -euo pipefail

TARGET=${TARGET:-"http://localhost:80/"}
DURATION=${DURATION:-"30s"}
CONNECTIONS=${CONNECTIONS:-"128"}
THREADS=${THREADS:-"4"}
RATE=${RATE:-"10000"}  # req/s for wrk2 constant-rate mode

check() {
  if ! command -v wrk2 &>/dev/null; then
    echo "wrk2 not found. Install: https://github.com/giltene/wrk2"
    exit 1
  fi
}

run_baseline() {
  local name=$1
  local url=$2
  echo "=== Baseline: $name ==="
  wrk2 -t "$THREADS" -c "$CONNECTIONS" -d "$DURATION" -R "$RATE" \
       --latency "$url"
  echo ""
}

check

echo "Omega-LB HTTP Benchmark"
echo "Target: $TARGET | Duration: $DURATION | Connections: $CONNECTIONS | Rate: ${RATE} rps"
echo "========================================================"

run_baseline "Omega-LB" "$TARGET"

# If Istio / NGINX are available on other ports, compare:
if [[ -n "${NGINX_TARGET:-}" ]]; then
  run_baseline "NGINX (baseline)" "$NGINX_TARGET"
fi
if [[ -n "${ISTIO_TARGET:-}" ]]; then
  run_baseline "Istio (baseline)" "$ISTIO_TARGET"
fi

echo "Done. Check results above."
echo "Key metrics: Throughput req/s, p50/p99/p999 latency"
echo "Success: Omega-LB should show ≥2× throughput and ≤50% p99 vs Istio"
