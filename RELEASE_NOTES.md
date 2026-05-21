# Omega-LB Desktop Release Notes

## Version 1.0.0-alpha.1 (May 21, 2026)

---

## Overview

**Omega-LB Desktop** is a professional-grade, native load balancer management application for macOS and Windows. It provides a unified GUI for deploying, configuring, monitoring, and managing Omega Load Balancer instances with zero command-line interaction required.

This is the **initial production-ready alpha release** featuring complete backend wiring, real-time health diagnostics, multi-layer routing, and comprehensive observability.

---

## What's Included

### Core Features

- **One-Click LB Deployment**
  - Start entire load balancer stack (proxy, backends, loadgen, metrics dashboard) with single button
  - Graceful shutdown with process monitoring and watchdog alerts
  - Subprocess management with automatic crash detection

- **Backend Wiring & Configuration**
  - Add/remove/edit backend targets with name, host, port, zone metadata
  - Real-time config persistence to `omega-lb.yaml`
  - "Load Demo Targets" pre-populates localhost backends for testing
  - Manual wiring for external real production backends

- **Backend Health Diagnostics**
  - Per-backend connection testing via HTTP GET to `/_health` endpoint
  - Latency measurement in milliseconds (e.g., "45.2ms")
  - Visual health status: UP (green), DOWN (red), FAIL, ERROR
  - "Test All Backends" bulk validation before starting stack
  - Non-blocking async threading prevents UI freeze

- **Live KPI Dashboard**
  - Real-time metrics updated every 1.5 seconds
  - Tick rate, RPS (requests/sec), total requests, error rate, healthy backend count
  - Backend telemetry table: 9 columns including latency, load, error %, vNodes, rate limiting, KAN weighting
  - Activity log with timestamped system events and diagnostic messages

- **Security Hardening**
  - Token-based admin authentication with cryptographically random secrets
  - CIDR-based IP allowlist for admin endpoints (default: 127.0.0.1/32, ::1/128)
  - Per-IP rate limiting on admin actions (default: 30 req/min)
  - Audit logging of blocked admin requests

- **Multi-Layer Routing Pipeline**
  - Layer 1: Consistent hash ring with consistent vNodes allocation
  - Layer 2: Health-aware ring with automatic failover
  - Layer 3: CBF (Connection-Based Filtering) for safety constraints
  - Layer 4: KAN (Kolmogorov-Arnold Network) ML inference for adaptive weighting
  - Layer 5: DQN+A3C rate limiting with proactive load distribution

- **Observability**
  - Streamlit-based metrics dashboard with real-time telemetry visualization
  - Backend latency heatmaps, error rate tracking, RPS distribution
  - vNode utilization insights
  - Optional auto-start dashboard on stack launch

- **Managed Mode**
  - Checkbox control: enable/disable auto-managed localhost backends
  - When enabled: automatically spawns backends at 127.0.0.1:9000-9003
  - Preflight validation warns if managed mode enabled but wiring doesn't match
  - Allows testing full stack locally without external services

- **Pre-Flight Validation**
  - Port availability checks (proxy: 8080, dashboard: 8501)
  - Backend host/port format validation
  - Managed mode wiring mismatch detection with user confirmation prompt
  - Clear error messages with actionable remediation steps

---

## What's NOT Included

### Known Limitations (Alpha Release)

- **No Persistent State Snapshots**
  - Load balancer state not automatically saved on shutdown
  - Backend metrics/logs not archived between sessions
  - Recommended: keep metrics dashboard window open for manual screenshot

- **No Advanced Admin UI**
  - Admin control panel (spike/kill/revive backends) not exposed in desktop GUI
  - Must use direct HTTP API calls to `POST /_omega/admin` for live backend manipulation
  - Token & allowlist configured via environment variables only

- **No TLS/HTTPS Support**
  - Desktop app to LB communication is HTTP-only (127.0.0.1 only, safe for localhost)
  - Backend connections use HTTP; no SSL/TLS certificate validation
  - Suitable for lab/dev environments; production deployments require reverse proxy + TLS termination

- **No Auto-Update Mechanism**
  - Must manually download and install new releases
  - No in-app update checker or staged rollout

- **Limited Scaling**
  - UI updates capped at 1.5s polling interval; may lag under extreme RPS (>1M/sec)
  - Backend table refresh rate tied to proxy status endpoint availability
  - Not suitable for monitoring >500 concurrent backend endpoints simultaneously

- **Windows Path Limitations** (Windows only)
  - YAML config stored in `omega-lb.yaml` (repo root); ensure write permissions
  - Spaces in deployment path may cause subprocess argument parsing issues
  - Network interface detection uses Windows API; non-standard configurations untested

- **No Kubernetes/Container Orchestration Integration**
  - No Helm charts, Kube manifests, or CRD integration
  - Standalone binary only; Docker deployment available separately via `docker-compose` in repo

- **No Multi-Region/Distributed Setup**
  - Single-instance LB control plane only
  - No consensus replication or geographic failover
  - Recommended for single-datacenter deployments

---

## System Requirements

### macOS

| Requirement | Minimum | Recommended |
|-------------|---------|-------------|
| OS Version | macOS 12 (Monterey) | macOS 13+ (Ventura/Sonoma) |
| Architecture | Apple Silicon (M1+) or Intel x64 | Apple Silicon (M2+) |
| RAM | 4 GB | 8+ GB |
| Disk Space | 500 MB | 2 GB |
| Network | Localhost only (no remote connections) | - |

**Download:** `OmegaLBDesktop-1.0.0-alpha.1-mac-arm64.dmg` (Apple Silicon) or `.dmg` (Intel x64)

### Windows

| Requirement | Minimum | Recommended |
|-------------|---------|-------------|
| OS Version | Windows 10 Build 19041+ | Windows 11 22H2+ |
| Architecture | x86-64 (64-bit only) | x86-64 with AVX2 support |
| RAM | 4 GB | 8+ GB |
| Disk Space | 500 MB | 2 GB |
| Runtime | .NET Framework 4.7+ (auto-bundled) | - |
| Visual C++ | 2015+ Redistributable | 2022 (Latest) |

**Download:** `OmegaLBDesktop-1.0.0-alpha.1-win-x64.msi` or `-portable.exe`

---

## Installation

### macOS

1. **Download** the `.dmg` file for your architecture from Releases
2. **Double-click** to mount the disk image
3. **Drag** `OmegaLBDesktop` into the **Applications** folder
4. **Launch**: Open Applications folder, find OmegaLBDesktop, double-click
5. **Grant permissions**: macOS will prompt to allow app on first launch (expected)

#### Troubleshooting
- **"App cannot be opened"**: Right-click → Open (to bypass Gatekeeper on first run)
- **Permission denied**: Run `xattr -d com.apple.quarantine ~/Applications/OmegaLBDesktop.app` in Terminal

### Windows

1. **Download** the `.msi` installer from Releases
2. **Double-click** the `.msi` file
3. **Follow installer wizard**: Select installation directory, agree to license
4. **Launch**: Windows Start Menu → OmegaLBDesktop or Start → type "Omega"
5. **Grant UAC permission** if prompted (required for port binding)

#### Portable Executable
- Alternatively, download `-portable.exe`, no installation needed
- Run directly; creates `OmegaLBDesktop.exe` in current folder
- Config/logs stored in same directory

---

## Quick Start Guide

### 1. Launch the App
- macOS: Applications → OmegaLBDesktop
- Windows: Start Menu → OmegaLBDesktop

### 2. Choose Your Setup

#### Option A: Test Locally (Managed Mode)
1. Check ✓ **Start local managed backends**
2. Click **Load Demo Targets** (auto-populates 127.0.0.1:9000-9003)
3. Click **Test All Backends** → verify all show "UP" (green)
4. Click **Start Stack**
5. Open **Dashboard** to watch live metrics
6. Open **Status API** to inspect JSON telemetry

#### Option B: Connect to External Backends
1. Uncheck ✗ **Start local managed backends**
2. Click **Backend Wiring** panel
3. Add your real backend: hostname/IP, port, zone
4. Click **Check** button → verify latency & status
5. Click **Save Wiring**
6. Click **Start Stack**
7. Proxy now routes to your real backends at http://127.0.0.1:8080

### 3. Monitor
- **KPI Tiles**: Watch Tick, RPS, Total Requests, Error Rate, Healthy Backend count
- **Backend Table**: See per-backend latency, load, error %, vNodes assigned, KAN weights, rate limits
- **Activity Log**: Real-time events (backend health changes, startup messages, errors)
- **Dashboard**: Streamlit visualization (click "Open Dashboard")

### 4. Troubleshoot
- **Backends showing RED**: Click **Check** to diagnose; verify host/port/network
- **Port 8080 in use**: Change proxy port in `omega-lb.yaml` before starting
- **Dashboard won't open**: Check that Streamlit (port 8501) is available
- **All services offline**: See Activity Log for error messages; check system logs

### 5. Stop
- Click **Stop Stack** → gracefully terminates all processes
- Wiring saved to `omega-lb.yaml` → auto-loads on next app launch

---

## Changelog

### 1.0.0-alpha.1 (May 21, 2026) - Initial Release

#### Added
- Native PySide6 desktop application (macOS/Windows)
- Backend Wiring panel: add/remove/save real backend targets
- Health diagnostics: per-backend check, latency measurement, visual status
- Live KPI dashboard: 5 real-time metrics + backend telemetry table
- Admin security: token auth + IP allowlist + rate limiting + audit logging
- Pre-flight validation: port checks, config validation, managed mode guards
- Multi-layer routing: consistent ring, health-aware failover, CBF, KAN inference, DQN rate limiting
- Metrics dashboard: Streamlit-based real-time observability
- Config persistence: automatic YAML load/save
- Process management: subprocess watchdog, crash alerts, graceful shutdown

#### Known Issues
- Alpha release; API surface may change
- Windows path validation incomplete (spaces in path not fully tested)
- TLS not supported; recommend reverse proxy for production
- No Kubernetes integration in this release

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│         OmegaLB Desktop v1.0.0-alpha.1              │
│  (PySide6 Native macOS/Windows Application)         │
├─────────────────────────────────────────────────────┤
│                                                     │
│  ┌─ Backend Wiring Panel ─────┐                   │
│  │ • Add/Remove targets       │ ◄─ omega-lb.yaml │
│  │ • Health Check per-row     │                   │
│  │ • Test All Backends        │                   │
│  └────────────────────────────┘                   │
│           ↓ Start Stack                           │
│  ┌─ Multi-Process Stack ─────────────────────┐   │
│  │ • Backends (9000-9003) ← HTTP servers     │   │
│  │ • Proxy (8080) ← 5-layer routing          │   │
│  │ • Loadgen ← synthetic traffic gen         │   │
│  │ • Dashboard (8501) ← Streamlit metrics    │   │
│  └───────────────────────────────────────────┘   │
│           ↓ Poll every 1.5s                      │
│  ┌─ Live Metrics Display ─────────────────┐     │
│  │ • KPI tiles: Tick, RPS, Total, Err%, OK │     │
│  │ • Backend table: latency, load, err%    │     │
│  │ • Activity log: timestamped events      │     │
│  └────────────────────────────────────────┘     │
│                                                     │
└─────────────────────────────────────────────────────┘
```

---

## Support & Feedback

- **Bug Reports**: [GitHub Issues](https://github.com/Vikx001/Load-Balancer-/issues)
- **Feature Requests**: [GitHub Discussions](https://github.com/Vikx001/Load-Balancer-/discussions)
- **Documentation**: See [README.md](README.md) for extended guides

---

## License

[Check LICENSE file in repository]

---

## Coming Soon (Post-Alpha)

- HTTPS/TLS support with certificate management
- MSI/DMG signed installers for auto-update
- Advanced admin UI panel (spike/kill/revive backends from GUI)
- State snapshots & metrics export (JSON/CSV/Prometheus)
- Multi-region/distributed control plane support
- Kubernetes integration (Helm charts, CRDs)
- Plugin system for custom routing policies

---

**Thank you for using Omega-LB Desktop!**  
_Making load balancing accessible, observable, and delightful._
