"""
4 real aiohttp HTTP backends — each has a distinct personality:
  Backend 0 (port 9000): Fast   ~45ms, low errors
  Backend 1 (port 9001): Normal ~52ms, normal errors
  Backend 2 (port 9002): Slow  ~120ms (simulates legacy service)
  Backend 3 (port 9003): Fast   ~38ms, lowest error rate

Environment variables (set dynamically by the proxy via /admin endpoint):
  BACKEND_{id}_LATENCY_MS  - base latency override
  BACKEND_{id}_ERROR_PCT   - error percentage override (0-100)
  BACKEND_{id}_OVERLOAD    - if "1", simulate CPU saturation
"""
import asyncio, aiohttp.web, random, time, json, os, sys, signal

BACKEND_HOST = os.environ.get("OMEGA_BACKEND_HOST", "127.0.0.1")

# Per-backend personality
PROFILES = [
    {"id": 0, "port": 9000, "base_ms": 45,  "jitter": 8,  "error_pct": 0.2},
    {"id": 1, "port": 9001, "base_ms": 52,  "jitter": 12, "error_pct": 0.3},
    {"id": 2, "port": 9002, "base_ms": 120, "jitter": 30, "error_pct": 0.5},
    {"id": 3, "port": 9003, "base_ms": 38,  "jitter": 5,  "error_pct": 0.1},
]

# Shared mutable state (per-process since each backend runs separately)
_state = {}


def make_app(profile: dict) -> aiohttp.web.Application:
    bid   = profile["id"]
    app   = aiohttp.web.Application()
    _state[bid] = {
        "base_ms":    profile["base_ms"],
        "jitter":     profile["jitter"],
        "error_pct":  profile["error_pct"],
        "overload":   False,
        "req_count":  0,
        "err_count":  0,
        "start_time": time.time(),
    }

    async def handle(request: aiohttp.web.Request) -> aiohttp.web.Response:
        s = _state[bid]
        s["req_count"] += 1

        # Simulate latency — M/M/1 blowup when overloaded
        base = s["base_ms"]
        if s["overload"]:
            base = base * 8 + random.gauss(0, base)   # near-saturation blowup
        lat_s = max(0.001, (base + random.gauss(0, s["jitter"])) / 1000)
        await asyncio.sleep(lat_s)

        # Error injection
        err_pct = s["error_pct"]
        if s["overload"]:
            err_pct += 15.0
        if random.random() * 100 < err_pct:
            s["err_count"] += 1
            return aiohttp.web.Response(
                status=503,
                text=json.dumps({
                    "backend":  bid,
                    "error":    "service_unavailable",
                    "latency_ms": lat_s * 1000,
                })
            )

        body = json.dumps({
            "backend":    bid,
            "req_id":     s["req_count"],
            "latency_ms": round(lat_s * 1000, 2),
            "uptime_s":   round(time.time() - s["start_time"], 1),
        })
        return aiohttp.web.Response(
            status=200,
            text=body,
            content_type="application/json"
        )

    async def handle_admin(request: aiohttp.web.Request) -> aiohttp.web.Response:
        """Admin endpoint for the proxy to control backend behaviour."""
        try:
            data = await request.json()
            s = _state[bid]
            if "base_ms"   in data: s["base_ms"]   = float(data["base_ms"])
            if "jitter"    in data: s["jitter"]     = float(data["jitter"])
            if "error_pct" in data: s["error_pct"]  = float(data["error_pct"])
            if "overload"  in data: s["overload"]   = bool(data["overload"])
            return aiohttp.web.Response(text="ok")
        except Exception as e:
            return aiohttp.web.Response(status=400, text=str(e))

    async def handle_stats(request: aiohttp.web.Request) -> aiohttp.web.Response:
        s = _state[bid]
        return aiohttp.web.Response(
            text=json.dumps({
                "backend":    bid,
                "req_count":  s["req_count"],
                "err_count":  s["err_count"],
                "error_rate": s["err_count"] / max(1, s["req_count"]),
                "base_ms":    s["base_ms"],
                "overload":   s["overload"],
            }),
            content_type="application/json"
        )

    async def handle_health(request: aiohttp.web.Request) -> aiohttp.web.Response:
        return aiohttp.web.Response(text="ok")

    app.router.add_get( "/_health",  handle_health)
    app.router.add_get( "/_stats",   handle_stats)
    app.router.add_post("/_admin",   handle_admin)
    app.router.add_route("*", "/{path_info:.*}", handle)
    return app


async def run_backend(profile: dict):
    app    = make_app(profile)
    runner = aiohttp.web.AppRunner(app)
    await runner.setup()
    site = aiohttp.web.TCPSite(runner, BACKEND_HOST, profile["port"])
    await site.start()
    print(f"[backends] Backend {profile['id']} -> http://{BACKEND_HOST}:{profile['port']}  "
          f"(base {profile['base_ms']}ms, err {profile['error_pct']}%)")
    return runner


async def main():
    runners = []
    for p in PROFILES:
        runners.append(await run_backend(p))
    print("[backends] All 4 backends running.")
    try:
        await asyncio.Event().wait()
    except (KeyboardInterrupt, asyncio.CancelledError):
        print("[backends] Shutting down...")
        for r in runners:
            await r.cleanup()


if __name__ == "__main__":
    asyncio.run(main())
