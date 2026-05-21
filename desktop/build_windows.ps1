Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

python -m venv .venv-desktop
.\.venv-desktop\Scripts\pip install -q -r requirements.txt -r desktop/requirements.txt

.\.venv-desktop\Scripts\pyinstaller `
  --noconfirm `
  --clean `
  --windowed `
  --name OmegaLBDesktop `
  --hidden-import demo.backends `
  --hidden-import demo.proxy `
  --hidden-import demo.loadgen `
  --hidden-import ml.kan `
  --hidden-import ml.cbf `
  --add-data "demo;demo" `
  --add-data "ml;ml" `
  --add-data "omega-lb.yaml;." `
  --add-data "omega-lb-docker.yaml;." `
  desktop/omegalb_desktop.py

Write-Host "Built executable: dist\OmegaLBDesktop\OmegaLBDesktop.exe"
