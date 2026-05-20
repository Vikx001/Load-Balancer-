"""
Omega-LB Real Demo Orchestrator
================================
Starts the full stack in the correct order, then tails the live output.

    .venv/bin/python3 demo/run.py

Press Ctrl-C to tear everything down cleanly.

Architecture:
  4 × aiohttp backend servers  (ports 9000-9003)
  1 × Omega-LB proxy           (port 8080, uses real H&A ring / KAN / CBF)
  1 × async load generator     (sinusoidal, 200 ± 80 rps)
  Streamlit dashboard          (port 8501, already running or start separately)

The proxy writes demo/live_metrics.json every second.
The dashboard reads it automatically when fresh (< 5 s old).
"""
import subprocess, sys, os, time, signal, threading, atexit

HERE    = os.path.dirname(os.path.abspath(__file__))
ROOT    = os.path.dirname(HERE)
VENV    = os.path.join(ROOT, ".venv", "bin", "python3")
PYTHON  = VENV if os.path.exists(VENV) else sys.executable

PROCESSES: list[subprocess.Popen] = []

LAUNCH_ORDER = [
    {
        "name":   "backends",
        "cmd":    [PYTHON, "-m", "demo.backends"],
        "wait_s": 1.5,   # give aiohttp time to bind all 4 ports
        "ready":  "All 4 backends running",
    },
    {
        "name":   "proxy",
        "cmd":    [PYTHON, "-m", "demo.proxy"],
        "wait_s": 1.0,
        "ready":  "Omega-LB proxy",
    },
    {
        "name":   "loadgen",
        "cmd":    [PYTHON, "-m", "demo.loadgen"],
        "wait_s": 0.5,
        "ready":  "Starting load generator",
    },
]


def _stream(proc: subprocess.Popen, label: str):
    """Read stdout/stderr and prefix with [label]."""
    for line in iter(proc.stdout.readline, b""):
        text = line.decode(errors="replace").rstrip()
        if text:
            print(f"[{label}] {text}", flush=True)


def _start(spec: dict) -> subprocess.Popen:
    print(f"\n{'─'*60}")
    print(f"  Starting: {spec['name']}")
    print(f"  CMD: {' '.join(spec['cmd'])}")
    print(f"{'─'*60}")

    proc = subprocess.Popen(
        spec["cmd"],
        cwd=ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        bufsize=1,
    )
    PROCESSES.append(proc)

    # Stream output in background thread
    t = threading.Thread(target=_stream, args=(proc, spec["name"]), daemon=True)
    t.start()

    # Wait a beat then check process is still alive
    time.sleep(spec["wait_s"])
    if proc.poll() is not None:
        print(f"\nERROR: {spec['name']} exited immediately (code {proc.returncode})!")
        _teardown()
        sys.exit(1)

    print(f"  {spec['name']} running (pid {proc.pid})")
    return proc


def _teardown():
    print("\n\nStopping all processes…")
    for proc in reversed(PROCESSES):
        try:
            proc.send_signal(signal.SIGINT)
        except Exception:
            pass
    for proc in PROCESSES:
        try:
            proc.wait(timeout=4)
        except subprocess.TimeoutExpired:
            proc.kill()
    # Clean up spike flag
    flag = os.path.join(HERE, "spike.flag")
    if os.path.exists(flag):
        os.remove(flag)
    print("All processes stopped.")


atexit.register(_teardown)


def _check_dashboard():
    """Check if Streamlit is already up; if not, print the launch command."""
    try:
        import urllib.request
        urllib.request.urlopen("http://localhost:8501", timeout=1)
        print("\n  Dashboard already running -> http://localhost:8501")
    except Exception:
        print(
            "\n  Dashboard not detected. Open a new terminal and run:\n"
            f"     {PYTHON} -m streamlit run dashboard/app.py\n"
            "   Then open http://localhost:8501 — it will switch to LIVE data automatically."
        )


def main():
    print("=" * 60)
    print("  Omega-LB Real Demo")
    print("=" * 60)
    print(f"  Python:  {PYTHON}")
    print(f"  Root:    {ROOT}")
    print("=" * 60)

    # Verify aiohttp is installed
    try:
        import aiohttp  # noqa: F401
    except ImportError:
        print("ERROR: aiohttp not found. Run:  pip install aiohttp")
        sys.exit(1)

    for spec in LAUNCH_ORDER:
        _start(spec)

    _check_dashboard()

    print("\n" + "=" * 60)
    print("  Full stack is running!")
    print("  Proxy  → http://127.0.0.1:8080")
    print("  Status → http://127.0.0.1:8080/_omega/status")
    print("  Dashboard → http://localhost:8501")
    print("=" * 60)
    print("\nPress Ctrl-C to stop everything.\n")

    try:
        while True:
            # Check all processes are still alive
            for proc in PROCESSES:
                if proc.poll() is not None:
                    name = next(
                        (s["name"] for s in LAUNCH_ORDER if PROCESSES.index(proc) == LAUNCH_ORDER.index(s)),
                        "unknown"
                    )
                    print(f"\nWARNING: Process {name} (pid {proc.pid}) exited unexpectedly.")
            time.sleep(5)
    except KeyboardInterrupt:
        pass
    finally:
        _teardown()


if __name__ == "__main__":
    main()
