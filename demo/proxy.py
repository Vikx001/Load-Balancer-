"""
Omega-LB Real Proxy — Port 8080
Uses the ACTUAL system components:
  - H&A consistent hash ring (MurmurHash3, 150 vnodes)
  - KAN symbolic equations (routing weights)
  - CBF safety projection (80% cap)
  - Token-bucket rate limiting per backend
  - Proactive vnode adjustment (30s lookahead)
  - Health checker (active HTTP probes)
"""
import asyncio, aiohttp, aiohttp.web
import sys, os, time, math, struct, json, threading
import numpy as np

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from demo.metrics_store import MetricsStore
from ml.kan import KANInference
from ml.cbf import CBFProjector as _CBFProjector, SafetyMonitor

# ─── Config loader ─────────────────────────────────────────────────────────────

def _load_config() -> dict:
    """Load omega-lb.yaml from repo root, fall back to sensible defaults."""
    root = os.path.join(os.path.dirname(__file__), "..")
    cfg_path = os.path.join(root, "omega-lb.yaml")
    defaults = {
        "proxy":        {"host": "127.0.0.1", "port": 8080},
        "backends":     [
            {"host": "127.0.0.1", "port": 9000, "name": "backend-0", "zone": "local-a"},
            {"host": "127.0.0.1", "port": 9001, "name": "backend-1", "zone": "local-b"},
            {"host": "127.0.0.1", "port": 9002, "name": "backend-2", "zone": "local-c"},
            {"host": "127.0.0.1", "port": 9003, "name": "backend-3", "zone": "local-a"},
        ],
        "kan":          {"model_path": "ml/models/kan_actor.onnx"},
        "cbf":          {"cap": 0.80, "lambda": 0.5},
        "rate_limiting":{"initial_rps": 1000, "min_rps": 100, "max_rps": 5000},
    }
    if not os.path.exists(cfg_path):
        return defaults
    try:
        import yaml  # PyYAML
        with open(cfg_path) as f:
            user = yaml.safe_load(f) or {}
    except ImportError:
        import json as _json
        # Fall back: try .json config or just use defaults
        json_path = os.path.join(root, "omega-lb.json")
        if os.path.exists(json_path):
            with open(json_path) as f:
                user = _json.load(f)
        else:
            print("[proxy] PyYAML not installed; using defaults. Run: pip install PyYAML")
            return defaults
    except Exception as e:
        print(f"[proxy] Could not parse omega-lb.yaml: {e} — using defaults")
        return defaults
    # Deep-merge user over defaults
    for k, v in user.items():
        if isinstance(v, dict) and k in defaults and isinstance(defaults[k], dict):
            defaults[k].update(v)
        else:
            defaults[k] = v
    return defaults

_CFG = _load_config()
_PROXY_HOST = _CFG["proxy"]["host"]
_PROXY_PORT = int(_CFG["proxy"]["port"])
BACKENDS = [
    (b["host"], int(b["port"]))
    for b in _CFG["backends"]
]
_BACKEND_NAMES = [b.get("name", f"backend-{i}") for i, b in enumerate(_CFG["backends"])]
_BACKEND_ZONES = [b.get("zone", "local")        for b in _CFG["backends"]]
N = len(BACKENDS)
_KAN_MODEL = os.path.join(os.path.dirname(__file__), "..", _CFG["kan"]["model_path"])
_CBF_CAP   = float(_CFG["cbf"]["cap"])
_CBF_LAM   = float(_CFG["cbf"].get("lambda", 0.5))
_INIT_RPS  = float(_CFG["rate_limiting"]["initial_rps"])
_MIN_RPS   = float(_CFG["rate_limiting"].get("min_rps", 100))
_MAX_RPS   = float(_CFG["rate_limiting"].get("max_rps", 5000))

print(f"[proxy] Loaded {N} backend(s) from config:")
for i, (host, port) in enumerate(BACKENDS):
    print(f"  [{i}] {_BACKEND_NAMES[i]}  {host}:{port}  ({_BACKEND_ZONES[i]})")

# ─── MurmurHash3 (matches ring.go) ────────────────────────────────────────────

def _murmur3_32(data: bytes, seed: int = 0) -> int:
    C1, C2 = 0xcc9e2d51, 0x1b873593
    h = seed & 0xFFFFFFFF
    length = len(data)
    for i in range(0, length - 3, 4):
        k = struct.unpack_from("<I", data, i)[0]
        k = (k * C1) & 0xFFFFFFFF
        k = ((k << 15) | (k >> 17)) & 0xFFFFFFFF
        k = (k * C2) & 0xFFFFFFFF
        h ^= k
        h = ((h << 13) | (h >> 19)) & 0xFFFFFFFF
        h = (h * 5 + 0xe6546b64) & 0xFFFFFFFF
    tail_len = length & 3
    if tail_len:
        tail = data[length - tail_len:] + b'\x00' * (4 - tail_len)
        k = struct.unpack_from("<I", tail)[0]
        k = (k * C1) & 0xFFFFFFFF
        k = ((k << 15) | (k >> 17)) & 0xFFFFFFFF
        k = (k * C2) & 0xFFFFFFFF
        h ^= k
    h ^= length
    h ^= h >> 16; h = (h * 0x85ebca6b) & 0xFFFFFFFF
    h ^= h >> 13; h = (h * 0xc2b2ae35) & 0xFFFFFFFF
    h ^= h >> 16
    return h


# ─── H&A Consistent Hash Ring (matches ring.go) ───────────────────────────────

class HAring:
    def __init__(self, n_backends: int, vnodes_per: int = 150):
        self._n       = n_backends
        self._vnodes  = np.array([float(vnodes_per)] * n_backends)
        self._health  = [True] * n_backends
        self._ring    = []   # [(hash_pos, backend_id)]
        self._lock    = threading.RLock()
        self._rebuild()

    def _rebuild(self):
        ring = []
        for i in range(self._n):
            if not self._health[i]:
                continue
            for v in range(int(self._vnodes[i])):
                key = f"{i}#{v}".encode()
                pos = _murmur3_32(key)
                ring.append((pos, i))
        ring.sort()
        self._ring = ring

    def set_vnodes(self, backend_id: int, count: int):
        with self._lock:
            self._vnodes[backend_id] = max(10, min(300, count))
            self._rebuild()

    def set_health(self, backend_id: int, healthy: bool):
        with self._lock:
            self._health[backend_id] = healthy
            self._rebuild()

    def route(self, key: str) -> int:
        with self._lock:
            if not self._ring:
                return 0
            h  = _murmur3_32(key.encode())
            lo, hi = 0, len(self._ring)
            while lo < hi:
                mid = (lo + hi) // 2
                if self._ring[mid][0] < h:
                    lo = mid + 1
                else:
                    hi = mid
            idx = lo % len(self._ring)
            return self._ring[idx][1]

    def vnode_counts(self) -> np.ndarray:
        with self._lock:
            return self._vnodes.copy()


# ─── KAN & CBF — use canonical ml/ modules ──────────────────────────────────
# KANInference: ONNX model when trained, symbolic fallback otherwise.
# SafetyMonitor: CBF projection with violation tracking.
# Both are imported at the top of this file from ml.kan / ml.cbf.


# ─── Token Bucket Rate Limiter ─────────────────────────────────────────────────

class TokenBucket:
    def __init__(self, rate: float):
        self._rate   = rate   # tokens/s
        self._tokens = rate
        self._last   = time.monotonic()
        self._lock   = threading.Lock()

    def consume(self) -> bool:
        with self._lock:
            now  = time.monotonic()
            self._tokens = min(self._rate, self._tokens + self._rate * (now - self._last))
            self._last   = now
            if self._tokens >= 1:
                self._tokens -= 1
                return True
            return False

    def set_rate(self, rate: float):
        with self._lock:
            self._rate = max(10, rate)


# ─── Health Checker ────────────────────────────────────────────────────────────

class HealthChecker:
    FAIL_THRESHOLD = 3

    def __init__(self, backends: list[tuple[str, int]], ring: HAring, metrics: MetricsStore):
        self._backends = backends
        self._ring     = ring
        self.metrics  = metrics
        self._fails    = [0] * len(backends)
        self._session  = None

    async def start(self):
        self._session = aiohttp.ClientSession(
            timeout=aiohttp.ClientTimeout(total=2)
        )
        asyncio.create_task(self._loop())

    async def _loop(self):
        while True:
            await asyncio.sleep(2)
            for i, (host, port) in enumerate(self._backends):
                try:
                    async with self._session.get(f"http://{host}:{port}/_health") as r:
                        if r.status == 200:
                            self._fails[i] = 0
                            if not self.metrics.health[i]:
                                print(f"[health] Backend {i} recovered ✅")
                                self._ring.set_health(i, True)
                                self.metrics.health[i] = True
                        else:
                            raise Exception(f"status {r.status}")
                except Exception as e:
                    self._fails[i] += 1
                    if self._fails[i] >= self.FAIL_THRESHOLD and self.metrics.health[i]:
                        print(f"[health] Backend {i} FAILED ❌ ({e})")
                        self._ring.set_health(i, False)
                        self.metrics.health[i] = False


# ─── Proactive Pre-distribution (matches ring.go ProactiveAdjust) ─────────────

class ProactiveController:
    def __init__(self, ring: HAring, metrics: MetricsStore):
        self._ring    = ring
        self.metrics = metrics

    def step(self):
        loads = self.metrics.load_hist[:, -10:]
        if loads.shape[1] < 10:
            return
        mean_load = loads.mean(axis=0)
        xs = np.arange(10, dtype=float) - 4.5
        denom = float(np.dot(xs, xs))
        slope = float(np.dot(xs, mean_load)) / denom if denom > 0 else 0

        if (slope * 30) > 0.75:
            self.metrics.proactive_active = True
            per_backend_load = self.metrics.load_hist[:, -1]
            for i in range(len(self.metrics.health)):
                if per_backend_load[i] > 0.70 and self.metrics.health[i]:
                    new_count = max(50, int(self._ring.vnode_counts()[i] * 0.97))
                    self._ring.set_vnodes(i, new_count)
        else:
            self.metrics.proactive_active = False
            # Restore vnodes toward 150
            counts = self._ring.vnode_counts()
            for i in range(len(counts)):
                if counts[i] < 148:
                    self._ring.set_vnodes(i, int(counts[i] * 0.99 + 150 * 0.01))

        self.metrics.vnode_counts = self._ring.vnode_counts()


# ─── DQN Rate Limit Adjuster (simplified online version) ──────────────────────

class DQNRateLimitAdjuster:
    """
    Simplified online DQN: adjusts token bucket rates every 100ms.
    Action space: {0: decrease 10%, 1: hold, 2: increase 10%}
    Uses epsilon-greedy with load-informed Q-values.
    """
    ACTIONS = [0.90, 1.00, 1.10]

    def __init__(self, buckets: list[TokenBucket], metrics: MetricsStore):
        self._buckets = buckets
        self.metrics = metrics
        self._step    = 0
        self._epsilon = 0.3

    def update(self):
        self._step += 1
        self._epsilon = max(0.05, 0.3 - self._step / 5000)
        loads = self.metrics.load_hist[:, -1]

        for i, bucket in enumerate(self._buckets):
            if not self.metrics.health[i]:
                continue
            load = loads[i]
            # Q-values: prefer decrease when overloaded, increase when underloaded
            if load > 0.80:
                q = [1.0, 0.2, 0.0]    # decrease preferred
            elif load > 0.65:
                q = [0.3, 0.6, 0.1]
            else:
                q = [0.0, 0.3, 0.7]    # increase preferred

            if np.random.random() < self._epsilon:
                action = np.random.randint(3)
            else:
                action = int(np.argmax(q))

            current_rate = self.metrics.rate_limits[i]
            new_rate = np.clip(current_rate * self.ACTIONS[action], _MIN_RPS, _MAX_RPS)
            self.metrics.rate_limits[i] = new_rate
            bucket.set_rate(new_rate)


# ─── Main Proxy ────────────────────────────────────────────────────────────────

class OmegaLBProxy:
    def __init__(self):
        self.metrics    = MetricsStore(N,
                                       names=_BACKEND_NAMES,
                                       zones=_BACKEND_ZONES)
        self.ring       = HAring(N, vnodes_per=150)
        self.kan        = KANInference.load(_KAN_MODEL)
        self.cbf        = SafetyMonitor(n_backends=N, cap=_CBF_CAP, lam=_CBF_LAM)
        self.buckets    = [TokenBucket(rate=_INIT_RPS) for _ in range(N)]
        self.health_chk = HealthChecker(BACKENDS, self.ring, self.metrics)
        self.proactive  = ProactiveController(self.ring, self.metrics)
        self.dqn        = DQNRateLimitAdjuster(self.buckets, self.metrics)
        self._session   = None
        self._req_count = 0

    async def start(self):
        self._session = aiohttp.ClientSession(
            connector=aiohttp.TCPConnector(limit=500),
            timeout=aiohttp.ClientTimeout(total=10)
        )
        await self.health_chk.start()
        asyncio.create_task(self._control_loop())

    async def _control_loop(self):
        """500ms control loop: KAN → CBF → ring update → proactive → DQN."""
        while True:
            await asyncio.sleep(0.5)
            loads  = self.metrics.load_hist[:, -1].copy()
            lats   = self.metrics.latency_hist[:, -1].copy()
            errors = self.metrics.error_hist[:, -1].copy()
            health = list(self.metrics.health)

            # Layer 3: KAN symbolic routing weights
            weights = self.kan.infer(loads, lats, errors, health)

            # Layer 2: CBF safety projection
            weights, cbf_active = self.cbf.step(weights, loads)
            self.metrics.cbf_active  = cbf_active
            self.metrics.kan_weights = weights

            # Apply weights to ring (vnode counts)
            for i, w in enumerate(weights):
                count = max(10, int(w * 150 * N))
                self.ring.set_vnodes(i, count)
            self.metrics.vnode_counts = self.ring.vnode_counts()

            # Layer 5: proactive pre-distribution
            self.proactive.step()

            # Layer 4: DQN rate limit update
            self.dqn.update()

    async def handle(self, request: aiohttp.web.Request) -> aiohttp.web.Response:
        self._req_count += 1
        routing_key = f"{request.remote}:{request.path}:{self._req_count % 1000}"

        # Select backend via H&A ring
        backend_id = self.ring.route(routing_key)

        # Rate limit check
        if not self.buckets[backend_id].consume():
            return aiohttp.web.Response(
                status=429,
                text=json.dumps({"error": "rate_limited", "backend": backend_id})
            )

        host, port = BACKENDS[backend_id]
        url = f"http://{host}:{port}{request.path_qs}"

        self.metrics.record_request_start(backend_id)
        t_start = time.monotonic()

        try:
            headers = {k: v for k, v in request.headers.items()
                       if k.lower() not in ("host", "content-length")}
            body = await request.read()

            async with self._session.request(
                request.method, url,
                headers=headers,
                data=body or None,
            ) as resp:
                latency_ms = (time.monotonic() - t_start) * 1000
                resp_body  = await resp.read()
                is_error   = resp.status >= 500
                self.metrics.record_request_end(backend_id, latency_ms, is_error,
                                                  len(resp_body))

                # Inject routing metadata into response headers
                resp_headers = {
                    "X-Omega-Backend":    str(backend_id),
                    "X-Omega-Latency-Ms": f"{latency_ms:.1f}",
                    "X-Omega-Ring-Pos":   f"{self.ring.vnode_counts()[backend_id]:.0f}v",
                }
                safe_headers = {}
                for k, v in resp.headers.items():
                    kl = k.lower()
                    if kl not in ("content-encoding", "transfer-encoding",
                                  "content-length", "connection"):
                        safe_headers[k] = v
                safe_headers.update(resp_headers)

                return aiohttp.web.Response(
                    status=resp.status,
                    body=resp_body,
                    headers=safe_headers,
                )
        except Exception as e:
            latency_ms = (time.monotonic() - t_start) * 1000
            self.metrics.record_request_end(backend_id, latency_ms, True)
            return aiohttp.web.Response(
                status=502,
                text=json.dumps({"error": str(e), "backend": backend_id})
            )

    async def handle_status(self, request: aiohttp.web.Request) -> aiohttp.web.Response:
        """Live JSON status endpoint."""
        snap = self.metrics.snapshot()
        snap["kan_equations"] = self.kan.equations(
            self.metrics.load_hist[:, -1],
            self.metrics.latency_hist[:, -1],
            self.metrics.error_hist[:, -1],
            self.metrics.health,
        )
        snap["kan_stats"] = self.kan.stats.to_dict()
        snap["cbf_audit"] = self.cbf.audit()
        return aiohttp.web.Response(
            text=json.dumps(snap, indent=2),
            content_type="application/json"
        )

    async def handle_admin(self, request: aiohttp.web.Request) -> aiohttp.web.Response:
        """Control endpoint for the dashboard fault-injection controls."""
        try:
            data = await request.json()
            # Kill/revive backend
            if "kill_backend" in data:
                i = int(data["kill_backend"])
                self.ring.set_health(i, False)
                self.metrics.health[i] = False
                # Tell the backend to simulate overload
                async with self._session.post(
                    f"http://127.0.0.1:{9000+i}/_admin",
                    json={"overload": True, "error_pct": 90}
                ) as _:
                    pass

            if "revive_backend" in data:
                i = int(data["revive_backend"])
                self.ring.set_health(i, True)
                self.metrics.health[i] = True
                async with self._session.post(
                    f"http://127.0.0.1:{9000+i}/_admin",
                    json={"overload": False, "error_pct": 0.3}
                ) as _:
                    pass

            if "spike" in data:
                # Tell load generator via shared file
                with open(os.path.join(os.path.dirname(__file__), "spike.flag"), "w") as f:
                    f.write("1" if data["spike"] else "0")

            return aiohttp.web.Response(text="ok")
        except Exception as e:
            return aiohttp.web.Response(status=400, text=str(e))


async def run_proxy():
    proxy = OmegaLBProxy()
    await proxy.start()

    app = aiohttp.web.Application()
    app.router.add_route("*", "/_omega/status", proxy.handle_status)
    app.router.add_post( "/_omega/admin",        proxy.handle_admin)
    app.router.add_route("*", "/{path_info:.*}", proxy.handle)

    runner = aiohttp.web.AppRunner(app)
    await runner.setup()
    site = aiohttp.web.TCPSite(runner, _PROXY_HOST, _PROXY_PORT)
    await site.start()
    print(f"[proxy] Omega-LB proxy → http://{_PROXY_HOST}:{_PROXY_PORT}")
    print(f"[proxy] Status          → http://{_PROXY_HOST}:{_PROXY_PORT}/_omega/status")
    return runner


async def main():
    runner = await run_proxy()
    try:
        await asyncio.Event().wait()
    except (KeyboardInterrupt, asyncio.CancelledError):
        print("[proxy] Shutting down...")
        await runner.cleanup()


if __name__ == "__main__":
    asyncio.run(main())
