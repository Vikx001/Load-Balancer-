"""
Shared metrics store — written by proxy, read by dashboard.
Uses an atomic JSON file so no inter-process locking is needed.
"""

import os, json, time, threading
import numpy as np

METRICS_FILE = os.path.join(os.path.dirname(__file__), "live_metrics.json")
N_BACKENDS = 4
HISTORY_LEN = 120  # 2 min at 1s ticks


class MetricsStore:
    """Thread-safe per-backend metrics collector inside the proxy process."""

    def __init__(self, n: int = N_BACKENDS, names: list | None = None, zones: list | None = None):
        self.n = n
        self.backend_names = names or [f"backend-{i}" for i in range(n)]
        self.backend_zones = zones or ["local"] * n
        self._lock = threading.Lock()
        # Rolling windows (last HISTORY_LEN seconds)
        self._lat_win = [[] for _ in range(n)]  # raw latency samples (ms)
        self._err_win = [[] for _ in range(n)]  # 1/0 per request
        self._req_win = [[] for _ in range(n)]  # request timestamps
        self._active = [0] * n  # in-flight connections
        self._total_req = [0] * n
        self._total_err = [0] * n
        self._total_bytes = [0] * n
        # History snapshots (1-per-second aggregated)
        self.latency_hist = np.zeros((n, HISTORY_LEN))
        self.load_hist = np.zeros((n, HISTORY_LEN))
        self.error_hist = np.zeros((n, HISTORY_LEN))
        self.rps_hist = np.zeros(HISTORY_LEN)
        self.vnode_counts = np.array([150.0] * n)
        self.health = [True] * n
        self.cbf_active = [False] * n
        self.rate_limits = np.array([1000.0] * n)
        self.kan_weights = np.ones(n) / n
        self.proactive_active = False
        self._tick = 0
        # Start aggregation thread
        t = threading.Thread(target=self._aggregator, daemon=True)
        t.start()

    def record_request_start(self, backend_id: int):
        with self._lock:
            self._active[backend_id] += 1

    def record_request_end(self, backend_id: int, latency_ms: float, is_error: bool, resp_bytes: int = 0):
        now = time.time()
        with self._lock:
            self._active[backend_id] = max(0, self._active[backend_id] - 1)
            self._lat_win[backend_id].append((now, latency_ms))
            self._err_win[backend_id].append((now, 1 if is_error else 0))
            self._req_win[backend_id].append(now)
            self._total_req[backend_id] += 1
            if is_error:
                self._total_err[backend_id] += 1
            self._total_bytes[backend_id] += resp_bytes

    def _prune(self, window, cutoff):
        return [(ts, v) for ts, v in window if ts > cutoff]

    def _aggregator(self):
        """Every second: compute per-backend stats, write JSON."""
        while True:
            time.sleep(1.0)
            self._aggregate_tick()
            self._write_json()

    def _aggregate_tick(self):
        now = time.time()
        cutoff = now - 2.0  # 2-second window for instantaneous stats
        with self._lock:
            lats = []
            loads = []
            errs = []
            total_rps = 0.0
            for i in range(self.n):
                self._lat_win[i] = self._prune(self._lat_win[i], now - 10)
                self._err_win[i] = self._prune(self._err_win[i], now - 10)
                self._req_win[i] = [ts for ts in self._req_win[i] if ts > now - 2]

                recent_lats = [v for ts, v in self._lat_win[i] if ts > cutoff]
                recent_errs = [v for ts, v in self._err_win[i] if ts > cutoff]
                recent_reqs = len(self._req_win[i])

                lat = float(np.mean(recent_lats)) if recent_lats else 0.0
                err = float(np.mean(recent_errs)) if recent_errs else 0.0
                rps_i = recent_reqs / 2.0
                # Load = active_connections / rate_limit (proxy for utilisation)
                load = min(1.0, (self._active[i] + rps_i / 50) / (self.rate_limits[i] / 50))

                lats.append(lat)
                loads.append(load)
                errs.append(err)
                total_rps += rps_i

        # Shift history buffers
        self.latency_hist = np.roll(self.latency_hist, -1, axis=1)
        self.load_hist = np.roll(self.load_hist, -1, axis=1)
        self.error_hist = np.roll(self.error_hist, -1, axis=1)
        self.rps_hist = np.roll(self.rps_hist, -1)
        for i in range(self.n):
            self.latency_hist[i, -1] = lats[i]
            self.load_hist[i, -1] = loads[i]
            self.error_hist[i, -1] = errs[i]
        self.rps_hist[-1] = total_rps

        # Proactive: slope check on last 10s
        if self._tick > 10:
            recent_load = self.load_hist[:, -10:].mean(axis=0)
            xs = np.arange(10, dtype=float) - 4.5
            denom = float(np.dot(xs, xs))
            slope = float(np.dot(xs, recent_load)) / denom if denom > 0 else 0
            self.proactive_active = (slope * 30) > 0.75

        self._tick += 1

    def snapshot(self) -> dict:
        with self._lock:
            active = list(self._active)
            total_req = list(self._total_req)
            total_err = list(self._total_err)
        return {
            "tick": self._tick,
            "n_backends": self.n,
            "backend_names": self.backend_names,
            "backend_zones": self.backend_zones,
            "latency_hist": self.latency_hist.tolist(),
            "load_hist": self.load_hist.tolist(),
            "error_hist": self.error_hist.tolist(),
            "rps_hist": self.rps_hist.tolist(),
            "vnode_counts": self.vnode_counts.tolist(),
            "health": self.health,
            "cbf_active": self.cbf_active,
            "rate_limits": self.rate_limits.tolist(),
            "kan_weights": self.kan_weights.tolist(),
            "proactive_active": self.proactive_active,
            "active_conns": active,
            "total_requests": total_req,
            "total_errors": total_err,
            "ts": time.time(),
        }

    def _write_json(self):
        data = self.snapshot()
        tmp = METRICS_FILE + ".tmp"
        try:
            with open(tmp, "w") as f:
                json.dump(data, f)
            os.replace(tmp, METRICS_FILE)  # atomic
        except Exception:
            pass


def read_live_metrics() -> dict | None:
    """Read the latest metrics written by the proxy. Returns None if stale/absent."""
    try:
        with open(METRICS_FILE) as f:
            data = json.load(f)
        if time.time() - data.get("ts", 0) > 5:
            return None
        return data
    except Exception:
        return None
