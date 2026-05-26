import os
from contextlib import nullcontext


_STATE = {
    "initialized": False,
    "metrics_available": False,
    "metrics": None,
    "metrics_port": None,
    "tracing_available": False,
    "otel_endpoint": None,
    "provider": None,
    "tracer": None,
}


def init_observability():
    if _STATE["initialized"]:
        return _STATE

    _STATE["initialized"] = True

    try:
        from prometheus_client import Gauge, start_http_server

        metrics_port = int(os.environ.get("OMEGALB_METRICS_PORT", "8001"))
        start_http_server(metrics_port)
        _STATE["metrics"] = {
            "requests_per_sec": Gauge("omegalb_dashboard_requests_per_sec", "Current requests per second"),
            "avg_latency_ms": Gauge("omegalb_dashboard_avg_latency_ms", "Average latency (ms)"),
            "error_rate_pct": Gauge("omegalb_dashboard_error_rate_pct", "Error rate (%)"),
            "healthy_targets": Gauge("omegalb_dashboard_healthy_targets", "Healthy targets"),
            "total_targets": Gauge("omegalb_dashboard_total_targets", "Total targets"),
            "cbf_count": Gauge("omegalb_dashboard_cbf_count", "CBF active count"),
            "proactive_active": Gauge("omegalb_dashboard_proactive_active", "Proactive redistribution active (0/1)"),
            "sla_pct": Gauge("omegalb_dashboard_sla_pct", "SLA percentage"),
        }
        _STATE["metrics_available"] = True
        _STATE["metrics_port"] = metrics_port
    except Exception:
        _STATE["metrics_available"] = False

    try:
        from opentelemetry import trace
        from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
        from opentelemetry.sdk.resources import Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor

        otel_endpoint = os.environ.get("OMEGALB_OTEL_ENDPOINT", "http://localhost:4318/v1/traces")
        provider = TracerProvider(resource=Resource.create({"service.name": "omegalb-dashboard"}))
        exporter = OTLPSpanExporter(endpoint=otel_endpoint)
        provider.add_span_processor(BatchSpanProcessor(exporter))
        trace.set_tracer_provider(provider)

        _STATE["provider"] = provider
        _STATE["tracer"] = trace.get_tracer("omegalb.dashboard")
        _STATE["tracing_available"] = True
        _STATE["otel_endpoint"] = otel_endpoint
    except Exception:
        _STATE["tracing_available"] = False

    return _STATE


def get_observability_status():
    state = init_observability()
    return {
        "metrics_available": state["metrics_available"],
        "metrics_port": state["metrics_port"],
        "tracing_available": state["tracing_available"],
        "otel_endpoint": state["otel_endpoint"],
    }


def update_metrics_from_state(cur_rps, avg_lat, avg_err, n_up, total_targets, cbf_cnt, proactive, sla_pct):
    state = init_observability()
    metrics = state["metrics"]
    if not state["metrics_available"] or metrics is None:
        return

    metrics["requests_per_sec"].set(cur_rps)
    metrics["avg_latency_ms"].set(avg_lat)
    metrics["error_rate_pct"].set(avg_err)
    metrics["healthy_targets"].set(n_up)
    metrics["total_targets"].set(total_targets)
    metrics["cbf_count"].set(cbf_cnt)
    metrics["proactive_active"].set(1 if proactive else 0)
    metrics["sla_pct"].set(sla_pct)


def maybe_traced(name):
    tracer = init_observability()["tracer"]
    if tracer is None:
        return nullcontext()
    return tracer.start_as_current_span(name)


def shutdown_observability():
    provider = init_observability()["provider"]
    if provider is not None:
        provider.shutdown()
