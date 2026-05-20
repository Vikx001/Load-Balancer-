#!/usr/bin/env bash
# ────────────────────────────────────────────────────────────────────────────
# Omega-LB reset
#
# Kills all local demo processes and returns the workspace to a clean state.
# Safe to run at any time — idempotent.
#
# Usage:
#   bash scripts/reset.sh
#   make reset
# ────────────────────────────────────────────────────────────────────────────
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info() { echo -e "${YELLOW}[reset]${NC} $*"; }
ok()   { echo -e "${GREEN}[  ok  ]${NC} $*"; }

# ── Kill demo processes ───────────────────────────────────────────────────────
info "Killing demo processes …"

killed=0
for pattern in "demo/proxy.py" "demo/backends.py" "demo/run.py" "streamlit"; do
    if pgrep -f "$pattern" >/dev/null 2>&1; then
        pkill -f "$pattern" 2>/dev/null && killed=$((killed+1)) || true
    fi
done

# Allow processes to exit cleanly
[ "$killed" -gt 0 ] && sleep 0.5

ok "Killed ${killed} process group(s)"

# ── Remove stale log files ────────────────────────────────────────────────────
info "Removing stale logs …"
removed=0
for f in proxy.log proxy-smoke.log demo/*.log; do
    [ -f "$f" ] && rm -f "$f" && removed=$((removed+1)) || true
done
ok "Removed ${removed} log file(s)"

# ── Reset metrics file ────────────────────────────────────────────────────────
info "Resetting demo/live_metrics.json …"
mkdir -p demo
echo '{}' > demo/live_metrics.json
ok "demo/live_metrics.json reset to {}"

# ── Clear Python cache in demo/ml dirs ───────────────────────────────────────
info "Clearing __pycache__ …"
find demo ml tests -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
ok "Python caches cleared"

echo ""
echo -e "${GREEN}Reset complete.${NC}  Run ${YELLOW}make dev${NC} to start fresh."
