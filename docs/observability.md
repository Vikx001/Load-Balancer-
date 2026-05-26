## Observability (Metrics & Tracing)

This repository exposes optional Prometheus metrics from the dashboard and can export OpenTelemetry traces to an OTLP collector.

Prometheus metrics
- The dashboard will start a small Prometheus metrics HTTP server on `OMEGALB_METRICS_PORT` (default `8001`) if `prometheus-client` is installed.
- Default metrics exposed (prefix `omegalb_dashboard_`):
  - `omegalb_dashboard_requests_per_sec`
  - `omegalb_dashboard_avg_latency_ms`
  - `omegalb_dashboard_error_rate_pct`
  - `omegalb_dashboard_healthy_targets`
  - `omegalb_dashboard_total_targets`
  - `omegalb_dashboard_cbf_count`
  - `omegalb_dashboard_proactive_active`
  - `omegalb_dashboard_sla_pct`

Tracing (OpenTelemetry)
- If OpenTelemetry packages are installed and `OMEGALB_OTEL_ENDPOINT` is set (default `http://localhost:4318/v1/traces`), the dashboard will export traces to that endpoint using the OTLP HTTP exporter. Spans are created for key operations like `_load_data` and `_step_sim`.

Grafana
- Import `deploy/monitoring/grafana-omegalb-dashboard.json` into Grafana and map the `DS_PROMETHEUS` input to your Prometheus datasource.
- The dashboard includes ready-made panels for request rate, latency, error rate, SLA, healthy targets, and CBF/proactive control signals.

Docker demo stack
- `deploy/monitoring/docker-compose.yml` brings up the full demo path with the Python backends, proxy, load generator, Streamlit dashboard, Prometheus, Grafana, and an OTEL collector.
- Grafana is pre-provisioned with Prometheus as the default datasource and auto-loads the Omega-LB dashboard on startup.
- Prometheus loads alert rules from `deploy/monitoring/alerts.yml` for high latency, rising error rate, and low healthy-target count.
- Alertmanager is included and forwards alerts to a local webhook sink backed by `scripts/alert_sink.py`.
- If the local Docker daemon cannot build the demo image reliably, `deploy/monitoring/docker-compose.local.yml` runs only the observability services and scrapes a locally running dashboard on `host.docker.internal:8001`.

Quick start

1. Install dashboard dependencies:

```bash
pip install -r dashboard/requirements.txt
```

2. Run the dashboard (metrics are exposed automatically if available):

```bash
export OMEGALB_METRICS_PORT=8001
.venv/bin/streamlit run dashboard/app.py
```

3. Example Prometheus scrape config: see `deploy/monitoring/prometheus-omegalb-scrape.yml`.

4. For tracing, run an OTLP-compatible collector (or Jaeger via collector) and set `OMEGALB_OTEL_ENDPOINT` to the collector's OTLP HTTP endpoint.

5. Import the Grafana dashboard JSON:

```text
deploy/monitoring/grafana-omegalb-dashboard.json
```

6. Run the full observability demo stack:

```bash
docker compose -f deploy/monitoring/docker-compose.yml up --build
```

Or use the Make target:

```bash
make observability-demo
```

For local Python demo processes with containerized observability only:

```bash
make observability-local
```

Then open:
- Proxy: `http://localhost:8080`
- Dashboard: `http://localhost:8501`
- Prometheus: `http://localhost:9090`
- Grafana: `http://localhost:3000` (`admin` / `omega-demo`)
- Alertmanager: `http://localhost:9093`
- Alert sink: `http://localhost:8088/alerts`
- OTEL collector health: `http://localhost:13133`

CI smoke test
- The repository includes `scripts/observability_smoke.py`, which brings up a local OTLP receiver, initializes the dashboard observability helper, verifies `/metrics`, and confirms a trace export.
- GitHub Actions runs the same check in `.github/workflows/observability.yml`.

Notes
- The metrics and tracing integrations are optional and no-ops if the required packages aren't installed, so the dashboard continues to work without them.
- The Docker demo stack mounts `deploy/monitoring/prometheus.yml`, `deploy/monitoring/alerts.yml`, `deploy/monitoring/alertmanager.yml`, and `deploy/monitoring/otel-collector.yml` directly, so edits take effect on container restart.
