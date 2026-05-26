import importlib
import os
import socket
import sys
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))


def _free_port():
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


class _TraceHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        body_len = int(self.headers.get("Content-Length", "0"))
        _ = self.rfile.read(body_len)
        self.server.requests.append(self.path)
        self.server.trace_seen.set()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, fmt, *args):
        return


def _wait_for_metrics(metrics_port):
    url = f"http://127.0.0.1:{metrics_port}/metrics"
    last_error = None
    for _ in range(30):
        try:
            with urllib.request.urlopen(url, timeout=1) as response:
                payload = response.read().decode("utf-8")
            if "omegalb_dashboard_requests_per_sec" in payload:
                return payload
        except Exception as exc:
            last_error = exc
        time.sleep(0.2)
    raise RuntimeError(f"metrics endpoint did not become ready: {last_error}")


def _assert_metric_value(payload, metric_name, expected_value):
    for line in payload.splitlines():
        if line.startswith(metric_name + " "):
            actual_value = float(line.split()[-1])
            if abs(actual_value - expected_value) > 1e-6:
                raise AssertionError(f"metric {metric_name} expected {expected_value}, got {actual_value}")
            return
    raise AssertionError(f"metric {metric_name} missing from payload")


def main():
    metrics_port = _free_port()
    otel_port = _free_port()

    trace_server = ThreadingHTTPServer(("127.0.0.1", otel_port), _TraceHandler)
    trace_server.requests = []
    trace_server.trace_seen = threading.Event()
    trace_thread = threading.Thread(target=trace_server.serve_forever, daemon=True)
    trace_thread.start()

    os.environ["OMEGALB_METRICS_PORT"] = str(metrics_port)
    os.environ["OMEGALB_OTEL_ENDPOINT"] = f"http://127.0.0.1:{otel_port}/v1/traces"

    try:
        observability = importlib.import_module("dashboard.observability")
        observability.init_observability()
        status = observability.get_observability_status()

        if not status["metrics_available"]:
            raise RuntimeError("Prometheus metrics failed to initialize")
        if not status["tracing_available"]:
            raise RuntimeError("OpenTelemetry tracing failed to initialize")

        observability.update_metrics_from_state(
            cur_rps=321.0,
            avg_lat=42.5,
            avg_err=0.5,
            n_up=4,
            total_targets=4,
            cbf_cnt=1,
            proactive=True,
            sla_pct=99.95,
        )

        metrics_payload = _wait_for_metrics(metrics_port)
        _assert_metric_value(metrics_payload, "omegalb_dashboard_requests_per_sec", 321.0)
        _assert_metric_value(metrics_payload, "omegalb_dashboard_avg_latency_ms", 42.5)
        _assert_metric_value(metrics_payload, "omegalb_dashboard_healthy_targets", 4.0)

        with observability.maybe_traced("ci-observability-smoke"):
            time.sleep(0.05)
        observability.shutdown_observability()

        if not trace_server.trace_seen.wait(5):
            raise RuntimeError("no OTLP trace POST received")
        if "/v1/traces" not in trace_server.requests:
            raise RuntimeError(f"unexpected OTLP paths received: {trace_server.requests}")

        print("OK observability smoke passed")
    finally:
        trace_server.shutdown()
        trace_server.server_close()


if __name__ == "__main__":
    main()
