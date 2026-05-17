# Omega-LB

**A 5-layer AI-driven load balancer you can run locally — for free — to monitor live HTTP traffic in real time.**

Omega-LB sits in front of your backends as a transparent reverse proxy. It routes requests using a pipeline of research-grade algorithms: consistent hashing, control-barrier-function safety projection, KAN interpretable policy, DQN adaptive rate limiting, and proactive pre-distribution. A Streamlit dashboard gives you a live view of every layer — no cloud account, no licence, no agents to deploy.

> Synthesises 12 research papers (2024–2026). See [Research Foundations](#research-foundations).

---

## What it looks like

The dashboard auto-detects the running proxy and switches to **LIVE** mode (green dot). Without a proxy it falls back to a built-in **DEMO** simulation so you always have something to explore.

| Tab | What you see |
|---|---|
| **Overview** | KPI tiles with sparklines, circular utilisation gauges, RPS chart, live request log, registered-targets table, fault simulator |
| **Routing Policy** | KAN weight stacked-area chart, CBF symbolic equations per backend, traffic-distribution donut, consistent hash-ring vnode shares, balance factor |
| **Rate Control** | Per-backend DQN action tiles (Expanding / Holding / Throttling), token-bucket utilisation chart |
| **Health Checks** | Per-backend status cards, P50/P95/P99 bar chart, latency heatmap, health-probe log |
| **Setup** | Quick-start guide, `omega-lb.yaml` in-browser editor, architecture table |

---

## Quick start (standalone, no Linux required)

You only need **Python 3.11+** and your application running somewhere.

```bash
# 1 — clone
git clone https://github.com/your-org/omega-lb
cd omega-lb

# 2 — edit omega-lb.yaml with your real backend addresses
#     (defaults point to localhost:9000-9003)
nano omega-lb.yaml

# 3 — one command: creates venv, installs deps, starts proxy + dashboard
./start.sh
```

The proxy listens on **http://localhost:8080** — point your load generator or browser at that address.  
The dashboard opens on **http://localhost:8501** — it flips to LIVE automatically once the proxy is up.

Press `Ctrl+C` to stop both processes cleanly.

### Manual start (two terminals)

```bash
# Terminal 1 — install dependencies once, then start the proxy
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python demo/proxy.py          # logs to stdout

# Terminal 2 — dashboard (reads demo/live_metrics.json written by the proxy)
.venv/bin/streamlit run dashboard/app.py
```

---

## Configuration — omega-lb.yaml

All user-facing settings live in `omega-lb.yaml` at the repo root. Edit it and restart the proxy to apply changes. The **Setup** tab in the dashboard includes a live editor.

```yaml
proxy:
  host: "127.0.0.1"     # 0.0.0.0 to accept external traffic
  port: 8080

backends:
  - host: "192.168.1.10"
    port: 8001
    name: "api-prod-1"
    zone: "dc-a"
  - host: "192.168.1.11"
    port: 8001
    name: "api-prod-2"
    zone: "dc-b"

kan:
  model_path: "ml/models/kan_actor.onnx"   # symbolic fallback if absent

cbf:
  cap: 0.80        # max utilisation per backend (0.0 – 1.0)
  lambda: 0.5      # CBF class-K coefficient

rate_limiting:
  initial_rps: 1000
  min_rps: 100
  max_rps: 5000
```

Supports **2–8 backends**. Names and zones are displayed verbatim in the dashboard.

---

## How it works — the 5-layer pipeline

Every inbound request passes through these layers in order:

```
Incoming request
       │
       ▼
┌──────────────────────────────────────────────────────────┐
│  Layer 1 — H&A Consistent Hash Ring                      │
│  MurmurHash3 · 150 virtual nodes per backend             │
│  Demand-aware bounded-load redistribution (β = 1.25)     │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│  Layer 2 — CBF Safety Projection                         │
│  Control Barrier Function caps utilisation at 80%        │
│  w_i = max(0, 1 − 0.42·cpu − 0.31·lat − 10·err) × h_i  │
│  Projects weight vector onto safe simplex via OSQP       │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│  Layer 3 — KAN Inference (interpretable routing)         │
│  Kolmogorov–Arnold Network · B-spline edges              │
│  ONNX hot-reload · symbolic equation audit log           │
│  Graceful fallback to symbolic formula when no model     │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│  Layer 4 — DQN Adaptive Rate Limiting                    │
│  Per-backend token buckets · ε-greedy DQN policy         │
│  Actions: Expanding ↑ / Holding — / Throttling ↓        │
│  Rate clamp: [min_rps, max_rps] from config              │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────┐
│  Layer 5 — Proactive Pre-distribution                    │
│  30-second load-slope lookahead                          │
│  Rebalances vnode counts ahead of saturation             │
└───────────────────────┬──────────────────────────────────┘
                        │
                        ▼
                   Backend pool
```

### Metrics bus

`demo/metrics_store.py` runs a thread-safe rolling collector that snapshots every second and writes `demo/live_metrics.json` atomically. The dashboard reads this file; if it is stale (>5 s) or absent the dashboard falls back to simulation mode automatically.

Exported fields: `latency_hist`, `load_hist`, `error_hist`, `rps_hist`, `vnode_counts`, `health`, `cbf_active`, `rate_limits`, `kan_weights`, `proactive_active`, `active_conns`, `total_requests`, `total_errors`, `backend_names`, `backend_zones`.

---

## Full system architecture (production)

For production deployments Omega-LB adds a Layer 0 eBPF kernel data plane and a Go control-plane daemon:

```
┌──────────────────────────────────────────────────────────┐
│  Layer 0 — eBPF kernel data plane (XDP + sock_ops)       │
│  filter_manager → route_manager → lb_policy → relay      │
│  Per-service token bucket updated every 100 ms           │
└───────────────────────┬──────────────────────────────────┘
                        │ (layers 1–5 as above)
                        ▼
┌──────────────────────────────────────────────────────────┐
│  Go control-plane daemon                                 │
│  gRPC xDS · ONNX inference · health checker (2 s)        │
│  OTLP telemetry · KAN symbolic audit log                 │
└──────────────────────────────────────────────────────────┘
```

**Requires**: Linux 5.15+, Go 1.22+, clang 14+, libbpf 1.3+.

---

## Project structure

```
omega-lb.yaml        User config — backends, proxy address, CBF, rate limits
start.sh             One-command launcher (venv + proxy + dashboard)
requirements.txt     Python dependencies

demo/
  proxy.py           aiohttp reverse proxy — implements all 5 layers
  metrics_store.py   Thread-safe metrics collector, writes live_metrics.json
  backends.py        Example backend servers for local testing
  loadgen.py         HTTP load generator for local testing

dashboard/
  app.py             Streamlit dashboard — LIVE/DEMO unified data layer

ml/
  kan/               KAN inference module (ONNX + symbolic fallback)
  cbf/               CBF projector (runtime)
  ppo/               PPO + KAN actor training (Layer 2+3)
  dqn_a3c/           DQN + A3C rate limiter training (Layer 4)
  simulation/        LB environment for RL training

ebpf/                eBPF C programs (Layer 0, Linux only)
controlplane/        Go daemon, gRPC xDS, OTLP metrics
deploy/              Kubernetes DaemonSet, Docker Compose, bare-metal scripts
bench/               Benchmark harness (simulation + HTTP)
proto/               gRPC / protobuf definitions
tests/               Python unit tests (72 tests, 71 pass standalone)
```

---

## Running the dashboard alone (DEMO mode)

No proxy, no backends needed:

```bash
python3 -m venv .venv
.venv/bin/pip install streamlit plotly numpy
.venv/bin/streamlit run dashboard/app.py
```

The dashboard runs a built-in M/M/1 queueing simulation with fault injection controls so you can explore all 5 layers and the UI without any infrastructure.

---

## Development

### Run tests

```bash
.venv/bin/pip install -r requirements.txt pytest torch
python -m pytest tests/ -v --tb=short
```

### Train ML models

```bash
make train-ppo    # PPO + KAN actor  → ml/models/kan_actor.onnx
make train-dqn    # DQN + A3C rate limiter
make smoke-train  # 1000-step smoke test, no GPU needed
```

### Full build (Linux, production)

```bash
make build          # eBPF + Go control plane
make docker-run     # Docker Compose, all layers
make bench          # simulation benchmarks
make k8s-deploy     # Kubernetes DaemonSet
```

### All make targets

```
make help
```

---

## Research foundations

| Layer | Algorithm | Source |
|---|---|---|
| 0 | eBPF XDP + sock_ops data plane | XLB, 2026 |
| 1 | Demand-aware consistent hashing (H&A) | OPODIS 2024 |
| 2 | PPO + Control Barrier Function safe RL | Huawei / IEEE 2024 |
| 3 | Kolmogorov–Arnold Network interpretable policy | arXiv 2505.14459, 2025 |
| 4 | DQN + A3C adaptive rate limiting | arXiv 2511.03279 |
| 5 | Proactive pre-distribution via load-slope lookahead | Anticipation paper, 2025 |

---

## Licence

MIT
