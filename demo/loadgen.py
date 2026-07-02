"""
Real async load generator — hammers the proxy on port 8080.

Traffic pattern:
  • Base RPS ~ 200 + 80·sin(2π·t/60)   — 60-second sinusoidal wave
  • Occasional micro-spikes via spike.flag (written by dashboard admin panel)
  • Varied paths to exercise different hash-ring slots
  • Prints a live stats line every 5 seconds
"""

import asyncio, aiohttp, time, math, os, random, sys

PROXY_URL = os.environ.get("OMEGA_PROXY_URL", "http://127.0.0.1:8080")
SPIKE_FLAG = os.path.join(os.path.dirname(__file__), "spike.flag")

PATHS = [
    "/api/v1/users",
    "/api/v1/orders",
    "/api/v1/products",
    "/api/v1/search",
    "/api/v2/recommendations",
    "/static/main.js",
    "/static/style.css",
    "/health",
    "/metrics",
]

_stats = {
    "total": 0,
    "ok": 0,
    "err": 0,
    "lat_sum": 0.0,
    "window": [],  # (ts, status, latency_ms) last 5s
}


def _is_spike() -> bool:
    try:
        return open(SPIKE_FLAG).read().strip() == "1"
    except Exception:
        return False


def _target_rps(t: float) -> float:
    base = 200 + 80 * math.sin(2 * math.pi * t / 60)
    if _is_spike():
        base *= 3.0
    return max(10, base)


async def _fire_request(session: aiohttp.ClientSession, path: str):
    t0 = time.monotonic()
    try:
        async with session.get(f"{PROXY_URL}{path}") as resp:
            await resp.read()
            lat_ms = (time.monotonic() - t0) * 1000
            is_ok = resp.status < 500
            now = time.time()
            _stats["total"] += 1
            _stats["lat_sum"] += lat_ms
            _stats["window"].append((now, is_ok, lat_ms))
            if is_ok:
                _stats["ok"] += 1
            else:
                _stats["err"] += 1
    except Exception:
        _stats["err"] += 1
        _stats["total"] += 1


async def _printer():
    while True:
        await asyncio.sleep(5)
        now = time.time()
        # Prune window
        _stats["window"] = [(ts, ok, lat) for ts, ok, lat in _stats["window"] if ts > now - 5]
        win = _stats["window"]
        if win:
            rps = len(win) / 5
            ok = sum(1 for _, ok, _ in win if ok)
            succ = ok / len(win) * 100
            lats = [lat for _, _, lat in win]
            p50 = sorted(lats)[len(lats) // 2]
            p99 = sorted(lats)[int(len(lats) * 0.99)]
            print(
                f"[loadgen] RPS={rps:6.1f}  success={succ:5.1f}%  "
                f"p50={p50:6.1f}ms  p99={p99:7.1f}ms  "
                f"total={_stats['total']}"
            )
        else:
            print("[loadgen] Waiting for traffic…")


async def main():
    connector = aiohttp.TCPConnector(limit=600)
    timeout = aiohttp.ClientTimeout(total=8)
    asyncio.create_task(_printer())
    t_start = time.monotonic()

    async with aiohttp.ClientSession(connector=connector, timeout=timeout) as session:
        pending: set[asyncio.Task] = set()
        slot_start = time.monotonic()
        slot_count = 0

        print(f"[loadgen] Starting load generator -> {PROXY_URL}")
        print("[loadgen] Ctrl-C to stop.\n")

        try:
            while True:
                now = time.monotonic()
                t = now - t_start
                rps = _target_rps(t)

                # How many requests should we have fired this second-slot?
                elapsed = now - slot_start
                if elapsed >= 1.0:
                    slot_start = now
                    slot_count = 0

                target = int(rps * (elapsed if elapsed < 1.0 else 1.0))
                to_fire = target - slot_count

                for _ in range(max(0, to_fire)):
                    path = random.choice(PATHS)
                    # Vary client fingerprint to spread across ring
                    src = f"10.0.{random.randint(0, 3)}.{random.randint(1, 254)}"
                    task = asyncio.create_task(_fire_request(session, path))
                    pending.add(task)
                    task.add_done_callback(pending.discard)
                    slot_count += 1

                # Throttle: sleep proportionally to stay on rate
                sleep_s = max(0, 1.0 / max(rps, 1) * 0.5)
                await asyncio.sleep(sleep_s)

                # Limit pending queue size to avoid memory blowup
                if len(pending) > 2000:
                    _, pending = await asyncio.wait(pending, return_when=asyncio.FIRST_COMPLETED)

        except (KeyboardInterrupt, asyncio.CancelledError):
            print("[loadgen] Stopped. Draining inflight requests…")
            if pending:
                await asyncio.gather(*pending, return_exceptions=True)


if __name__ == "__main__":
    asyncio.run(main())
