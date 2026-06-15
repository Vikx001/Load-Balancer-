#!/usr/bin/env bash
# ────────────────────────────────────────────────────────────────────────────
# Omega-LB health check
#
# Probes every component of the demo stack and prints a status table.
# Designed to be run at any time (even when the stack is not running) to
# quickly see what is alive and what needs attention.
# This script is diagnostic only and does not modify any service state.
#
# Exit codes:
#   0 — all probed services are healthy
#   1 — at least one service is down or metrics are stale
#
# Ref: Google SRE Book — Chapter 6 "Monitoring Distributed Systems"
# ────────────────────────────────────────────────────────────────────────────
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || exit

METRICS_FILE="demo/live_metrics.json"
PROXY_PORT="${OMEGA_LB_PORT:-8080}"
DASHBOARD_PORT=8501
BACKEND_PORTS=(9000 9001 9002 9003)
STALE_WARN_SECS=30
STALE_CRIT_SECS=60
ALL_OK=true

# ── Colour helpers ────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'

status_line() {
    local name="$1" status="$2" detail="${3:-}"
    printf "  %-26s  %b\n" "$name" "$status  ${CYAN}${detail}${NC}"
}

# ── Probe a TCP port ─────────────────────────────────────────────────────────
probe_http() {
    local url="$1"
    local code
    code=$(curl -sf -o /dev/null -w "%{http_code}" --max-time 2 "$url" 2>/dev/null || echo "000")
    echo "$code"
}

is_up() {
    local code="$1"
    [[ "$code" =~ ^(200|204|301|302|404)$ ]]
}

echo ""
echo -e "${BOLD}${CYAN}━━━━━━━━━━━━━━━  Omega-LB Health Check  ━━━━━━━━━━━━━━━${NC}"
echo ""

# ── Proxy ─────────────────────────────────────────────────────────────────────
proxy_code=$(probe_http "http://127.0.0.1:${PROXY_PORT}/")
if is_up "$proxy_code"; then
    status_line "Proxy  :${PROXY_PORT}" "${GREEN}● HEALTHY${NC}" "HTTP ${proxy_code}"
else
    status_line "Proxy  :${PROXY_PORT}" "${RED}○ DOWN${NC}" "no response"
    ALL_OK=false
fi

# ── Dashboard ────────────────────────────────────────────────────────────────
dash_code=$(probe_http "http://127.0.0.1:${DASHBOARD_PORT}/")
if is_up "$dash_code"; then
    status_line "Dashboard :${DASHBOARD_PORT}" "${GREEN}● HEALTHY${NC}" "HTTP ${dash_code}"
else
    status_line "Dashboard :${DASHBOARD_PORT}" "${YELLOW}○ NOT RUNNING${NC}" "start: make dev"
fi

echo ""

# ── Backends ─────────────────────────────────────────────────────────────────
for port in "${BACKEND_PORTS[@]}"; do
    be_code=$(probe_http "http://127.0.0.1:${port}/health")
    if is_up "$be_code"; then
        lat=$(curl -sf -o /dev/null -w "%{time_total}" --max-time 2 \
              "http://127.0.0.1:${port}/" 2>/dev/null || echo "?")
        lat_ms=$(awk "BEGIN{printf \"%.0f\", $lat * 1000}" 2>/dev/null || echo "?")
        status_line "Backend :${port}" "${GREEN}● HEALTHY${NC}" "${lat_ms} ms RTT"
    else
        status_line "Backend :${port}" "${RED}○ DOWN${NC}" "no response"
        ALL_OK=false
    fi
done

echo ""

# ── Metrics file ─────────────────────────────────────────────────────────────
if [ ! -f "$METRICS_FILE" ]; then
    status_line "Metrics file" "${RED}✗ MISSING${NC}" "$METRICS_FILE"
    ALL_OK=false
elif ! python3 -c "import json; json.load(open('$METRICS_FILE'))" 2>/dev/null; then
    status_line "Metrics file" "${RED}✗ INVALID JSON${NC}" "$METRICS_FILE"
    ALL_OK=false
else
    # Determine age
    if [[ "$(uname)" == "Darwin" ]]; then
        mtime=$(stat -f %m "$METRICS_FILE" 2>/dev/null || echo 0)
    else
        mtime=$(stat -c %Y "$METRICS_FILE" 2>/dev/null || echo 0)
    fi
    now=$(date +%s)
    age=$((now - mtime))

    size=$(wc -c < "$METRICS_FILE" | tr -d ' ')
    if [ "$size" -lt 10 ]; then
        status_line "Metrics file" "${YELLOW}⚠ EMPTY${NC}" "no data yet (stack starting?)"
    elif [ "$age" -lt "$STALE_WARN_SECS" ]; then
        status_line "Metrics file" "${GREEN}● LIVE${NC}" "updated ${age}s ago"
    elif [ "$age" -lt "$STALE_CRIT_SECS" ]; then
        status_line "Metrics file" "${YELLOW}⚠ STALE${NC}" "last updated ${age}s ago"
    else
        status_line "Metrics file" "${RED}⚠ DEMO MODE${NC}" "not updated in ${age}s (proxy down?)"
    fi
fi

echo ""
echo -e "${BOLD}${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

if $ALL_OK; then
    echo -e "  ${GREEN}${BOLD}All required services healthy.${NC}"
    exit 0
else
    echo -e "  ${RED}${BOLD}One or more services are down.${NC}  Run: make dev"
    exit 1
fi
