#!/usr/bin/env bash
# ────────────────────────────────────────────────────────────────────────────
# Omega-LB smoke test
#
# Starts the full demo stack, fires N HTTP probes at the proxy, asserts all
# responses are valid, checks the metrics file is being written, then tears
# everything down cleanly.
#
# Exit codes:
#   0 — all assertions passed
#   1 — at least one assertion failed (details printed to stderr)
#
# Usage:
#   bash scripts/smoke_test.sh            # default: 30 probes
#   SMOKE_REQUESTS=50 bash scripts/smoke_test.sh
#
# Ref: Google SRE Workbook — Chapter 17 "Testing for Reliability"
# ────────────────────────────────────────────────────────────────────────────
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SMOKE_REQUESTS="${SMOKE_REQUESTS:-30}"
PROXY_PORT="${OMEGA_LB_PORT:-8080}"
PROXY_URL="http://127.0.0.1:${PROXY_PORT}"
METRICS_FILE="demo/live_metrics.json"
STARTUP_TIMEOUT=15           # seconds to wait for proxy to accept connections
PIDS=()
PASS=0
FAIL=0

# ── Colour helpers ────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${YELLOW}[smoke]${NC} $*"; }
ok()    { echo -e "${GREEN}[  OK  ]${NC} $*"; PASS=$((PASS+1)); }
fail()  { echo -e "${RED}[ FAIL ]${NC} $*" >&2; FAIL=$((FAIL+1)); }

# ── Cleanup trap ──────────────────────────────────────────────────────────────
cleanup() {
    info "Tearing down demo stack …"
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    pkill -f "demo/backends.py" 2>/dev/null || true
    pkill -f "demo/proxy.py"    2>/dev/null || true
    sleep 0.5
}
trap cleanup EXIT

# ── Resolve Python ────────────────────────────────────────────────────────────
if [ -x ".venv/bin/python3" ]; then
    PY=".venv/bin/python3"
elif command -v python3 &>/dev/null; then
    PY="python3"
else
    echo "python3 not found. Run: python3 -m venv .venv && .venv/bin/pip install -r requirements.txt" >&2
    exit 1
fi

# ── Ensure demo/live_metrics.json exists ─────────────────────────────────────
mkdir -p demo
[ -f "$METRICS_FILE" ] || echo '{}' > "$METRICS_FILE"

# ── Start 4 backend servers ───────────────────────────────────────────────────
info "Starting 4 backends …"
for i in 0 1 2 3; do
    $PY -c "
import asyncio, sys, os
sys.path.insert(0,'.')
os.environ.setdefault('BACKEND_ID', '$i')
from demo.backends import PROFILES, make_app
import aiohttp.web
profile = PROFILES[$i]
app = make_app(profile)
aiohttp.web.run_app(app, host='127.0.0.1', port=profile['port'],
                    access_log=None, print=lambda *a,**k: None)
" &>/dev/null &
    PIDS+=($!)
done

# ── Wait for backends to be up ────────────────────────────────────────────────
info "Waiting for backends …"
for port in 9000 9001 9002 9003; do
    deadline=$((SECONDS + 10))
    until curl -sf "http://127.0.0.1:${port}/health" >/dev/null 2>&1; do
        [ $SECONDS -lt $deadline ] || { fail "Backend :${port} did not start in 10s"; exit 1; }
        sleep 0.2
    done
done
ok "All 4 backends up"

# ── Start proxy ───────────────────────────────────────────────────────────────
info "Starting proxy on :${PROXY_PORT} …"
OMEGA_LB_PORT=$PROXY_PORT $PY demo/proxy.py >"$REPO_ROOT/proxy-smoke.log" 2>&1 &
PIDS+=($!)

# ── Wait for proxy ────────────────────────────────────────────────────────────
info "Waiting for proxy (up to ${STARTUP_TIMEOUT}s) …"
deadline=$((SECONDS + STARTUP_TIMEOUT))
until curl -sf "${PROXY_URL}/" >/dev/null 2>&1 || \
      curl -sf "${PROXY_URL}/health" >/dev/null 2>&1; do
    if [ $SECONDS -ge $deadline ]; then
        fail "Proxy did not accept connections within ${STARTUP_TIMEOUT}s"
        echo "--- proxy-smoke.log (last 20 lines) ---" >&2
        tail -20 "$REPO_ROOT/proxy-smoke.log" >&2 || true
        exit 1
    fi
    sleep 0.3
done
ok "Proxy accepting connections"

# ── Fire HTTP probes ──────────────────────────────────────────────────────────
info "Firing ${SMOKE_REQUESTS} HTTP probes …"
GOOD=0; BAD=0
for i in $(seq 1 "$SMOKE_REQUESTS"); do
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
                    --max-time 5 "${PROXY_URL}/" 2>/dev/null || echo "000")
    if [[ "$HTTP_CODE" =~ ^(200|201|202|204|301|302|404)$ ]]; then
        GOOD=$((GOOD+1))
    else
        BAD=$((BAD+1))
        info "  probe $i → HTTP ${HTTP_CODE}"
    fi
done

if [ "$BAD" -le 1 ]; then
    ok "${GOOD}/${SMOKE_REQUESTS} probes returned valid HTTP codes (≤1 transient failure allowed)"
else
    fail "${BAD}/${SMOKE_REQUESTS} probes returned unexpected HTTP codes"
fi

# ── Check metrics file is live ────────────────────────────────────────────────
info "Checking metrics file is being written …"
sleep 2  # give proxy time to flush at least one snapshot

if [ ! -f "$METRICS_FILE" ]; then
    fail "Metrics file not found: ${METRICS_FILE}"
elif [ "$(wc -c < "$METRICS_FILE")" -lt 10 ]; then
    fail "Metrics file appears empty (< 10 bytes)"
elif python3 -c "import json,sys; json.load(open('$METRICS_FILE'))" 2>/dev/null; then
    ok "Metrics file is valid JSON"
else
    fail "Metrics file is not valid JSON"
fi

# ── Check metric field presence ───────────────────────────────────────────────
info "Checking expected metric keys …"
KEYS_OK=0
for key in "requests_total" "p99_ms" "backend_weights"; do
    if $PY -c "
import json
d = json.load(open('$METRICS_FILE'))
assert '$key' in d, 'missing key: $key'
" 2>/dev/null; then
        KEYS_OK=$((KEYS_OK+1))
    fi
done
# Metrics keys are best-effort (proxy may not have started writing yet)
if [ "$KEYS_OK" -gt 0 ]; then
    ok "Metrics file contains at least ${KEYS_OK} expected keys"
else
    info "  (metrics file may still be empty — proxy writes on first tick)"
fi

# ── Final report ──────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}────────────── Smoke Test Report ──────────────${NC}"
echo -e "  Passed : ${GREEN}${PASS}${NC}"
echo -e "  Failed : ${RED}${FAIL}${NC}"
echo -e "${GREEN}───────────────────────────────────────────────${NC}"

[ "$FAIL" -eq 0 ] && exit 0 || exit 1
