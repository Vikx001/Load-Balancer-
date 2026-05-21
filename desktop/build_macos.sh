#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

python3 -m venv .venv-desktop
.venv-desktop/bin/pip install -q -r requirements.txt -r desktop/requirements.txt

.venv-desktop/bin/pyinstaller \
  --noconfirm \
  --windowed \
  --name OmegaLBDesktop \
  --add-data "demo:demo" \
  --add-data "ml:ml" \
  --add-data "omega-lb.yaml:." \
  --add-data "omega-lb-docker.yaml:." \
  desktop/omegalb_desktop.py

echo "Built app bundle: dist/OmegaLBDesktop.app"
