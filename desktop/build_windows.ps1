Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

python -m venv .venv-desktop
.\.venv-desktop\Scripts\pip install -q -r requirements.txt -r desktop/requirements.txt

.\.venv-desktop\Scripts\pyinstaller `
  --noconfirm `
  --windowed `
  --name OmegaLBDesktop `
  --add-data "demo;demo" `
  --add-data "ml;ml" `
  --add-data "omega-lb.yaml;." `
  --add-data "omega-lb-docker.yaml;." `
  desktop/omegalb_desktop.py

Write-Host "Built executable: dist\OmegaLBDesktop\OmegaLBDesktop.exe"
