#!/usr/bin/env bash
# ────────────────────────────────────────────────────────────────────────────
# Omega-LB — one-command launcher
# Usage: ./start.sh [--port 8080] [--dashboard-port 8501]
# ────────────────────────────────────────────────────────────────────────────
set -euo pipefail
cd "$(dirname "$0")"

PROXY_PORT=8080
DASH_PORT=8501

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)          PROXY_PORT="$2"; shift 2 ;;
    --dashboard-port) DASH_PORT="$2";  shift 2 ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

# ── Virtual environment ────────────────────────────────────────────────────────
if [ ! -d ".venv" ]; then
  echo "[omega-lb] Creating virtual environment..."
  python3 -m venv .venv
fi

echo "[omega-lb] Installing dependencies..."
.venv/bin/pip install -q -r requirements.txt

# ── Kill any previous instances ────────────────────────────────────────────────
pkill -f "python.*demo/proxy.py" 2>/dev/null && echo "[omega-lb] Stopped previous proxy" || true
pkill -f "streamlit run dashboard/app.py" 2>/dev/null && echo "[omega-lb] Stopped previous dashboard" || true
sleep 0.5

# ── Start proxy ────────────────────────────────────────────────────────────────
echo "[omega-lb] Starting proxy on :${PROXY_PORT}..."
OMEGA_LB_PORT=$PROXY_PORT .venv/bin/python demo/proxy.py > proxy.log 2>&1 &
PROXY_PID=$!
echo "[omega-lb] Proxy PID: ${PROXY_PID}"

# Wait until the proxy is accepting connections (up to 8s)
for i in $(seq 1 8); do
  if curl -sf "http://127.0.0.1:${PROXY_PORT}/_omega/status" > /dev/null 2>&1; then
    echo "[omega-lb] Proxy ready ✓"
    break
  fi
  sleep 1
  if [ $i -eq 8 ]; then
    echo "[omega-lb] Proxy not responding after 8s — check proxy.log"
  fi
done

# ── Start dashboard ────────────────────────────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Omega-LB is running"
echo "  Proxy:     http://127.0.0.1:${PROXY_PORT}"
echo "  Dashboard: http://localhost:${DASH_PORT}"
echo "  Logs:      ./proxy.log"
echo "  Stop:      Ctrl+C"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

trap "echo; echo '[omega-lb] Shutting down...'; kill $PROXY_PID 2>/dev/null || true; exit 0" INT TERM

.venv/bin/streamlit run dashboard/app.py \
  --server.port "$DASH_PORT" \
  --server.address 0.0.0.0 \
  --server.headless true \
  --browser.gatherUsageStats false \
  --browser.serverAddress localhost
