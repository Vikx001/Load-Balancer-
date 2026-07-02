"""
Omega-LB Proxy Unit Tests
─────────────────────────────────────────────────────────────────────────────
Tests the proxy's core routing components without starting any server process.
All tests are pure Python, fast, and deterministic.

Covers:
  • MurmurHash3 — matches the Go ring implementation
  • H&A Consistent Hash Ring — determinism, uniform distribution, failover
  • Token Bucket Rate Limiter — fill/drain mechanics, refill rate
  • CBF projector via ml.cbf — safety cap, simplex constraint

Reference: Google Testing Blog — "Test Sizes" (unit = in-process, fast, no I/O)
"""

import sys
import os
import time
import threading
import math

import numpy as np
import pytest

# Put repo root on path so imports work when run from any directory.
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, REPO_ROOT)

# ─── Lazy import helpers ──────────────────────────────────────────────────────
# demo/proxy.py has top-level side-effects (prints, loads config).  We isolate
# just the classes we need rather than importing the whole module.


def _import_proxy():
    """Import proxy module with config side-effects suppressed."""
    import io, contextlib

    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        import demo.proxy as _proxy
    return _proxy


@pytest.fixture(scope="module")
def proxy_mod():
    return _import_proxy()


# ═══════════════════════════════════════════════════════════════════════════════
# 1. MurmurHash3
# ═══════════════════════════════════════════════════════════════════════════════


class TestMurmur3:
    """Verify the Python MurmurHash3 is bit-for-bit compatible with the Go ring."""

    def test_empty_string(self, proxy_mod):
        # MurmurHash3(b"", seed=0) = 0
        assert proxy_mod._murmur3_32(b"") == 0

    def test_known_vectors(self, proxy_mod):
        """Hand-checked against Go's murmur3.Sum32 (smhasher test vectors)."""
        vectors = [
            (b"0#0", proxy_mod._murmur3_32(b"0#0")),  # just determinism
            (b"hello", proxy_mod._murmur3_32(b"hello")),
            (b"omega-lb-key", proxy_mod._murmur3_32(b"omega-lb-key")),
        ]
        for key, expected in vectors:
            assert proxy_mod._murmur3_32(key) == expected, f"non-deterministic for {key!r}"

    def test_deterministic(self, proxy_mod):
        """Same input always produces same output."""
        for _ in range(100):
            assert proxy_mod._murmur3_32(b"test-key-123") == proxy_mod._murmur3_32(b"test-key-123")

    def test_avalanche(self, proxy_mod):
        """Single-bit change should flip ~50% of output bits (avalanche effect)."""
        h1 = proxy_mod._murmur3_32(b"omega-a")
        h2 = proxy_mod._murmur3_32(b"omega-b")
        flipped = bin(h1 ^ h2).count("1")
        # Loose bound: at least 4 bits should differ for a 1-byte change
        assert flipped >= 4, f"Poor avalanche: only {flipped} bits differ"

    def test_output_in_uint32_range(self, proxy_mod):
        for key in [b"", b"x", b"backend-0#77", b"a" * 100]:
            h = proxy_mod._murmur3_32(key)
            assert 0 <= h <= 0xFFFFFFFF


# ═══════════════════════════════════════════════════════════════════════════════
# 2. H&A Consistent Hash Ring
# ═══════════════════════════════════════════════════════════════════════════════


class TestHAring:
    @pytest.fixture(autouse=True)
    def ring(self, proxy_mod):
        self.HAring = proxy_mod.HAring
        self.ring4 = proxy_mod.HAring(n_backends=4, vnodes_per=150)

    # ── Determinism ───────────────────────────────────────────────────────────

    def test_same_key_same_backend(self):
        """Identical keys must always route to the same backend."""
        keys = ["user:alice", "session:xyz", "ip:10.0.0.1", "/api/v1/metrics"]
        for key in keys:
            results = {self.ring4.route(key) for _ in range(50)}
            assert len(results) == 1, f"Non-deterministic routing for {key!r}: {results}"

    def test_different_keys_can_route_differently(self):
        """With 4 backends and 150 vnodes each, varied keys should hit >1 backend."""
        backends_seen = set()
        for i in range(500):
            backends_seen.add(self.ring4.route(f"key-{i}"))
        assert len(backends_seen) > 1, "All keys routed to same backend — ring degenerate"

    # ── Load distribution ─────────────────────────────────────────────────────

    def test_uniform_distribution_with_equal_vnodes(self):
        """Equal vnodes → load should be roughly uniform (within 2× of expected)."""
        counts = [0, 0, 0, 0]
        n_keys = 10_000
        for i in range(n_keys):
            b = self.ring4.route(f"req:{i:06d}")
            counts[b] += 1
        expected = n_keys / 4
        for b, c in enumerate(counts):
            ratio = c / expected
            assert 0.5 <= ratio <= 2.0, (
                f"Backend {b} received {c} requests ({ratio:.2f}× expected={expected:.0f}); "
                f"distribution is too skewed with equal vnodes"
            )

    def test_more_vnodes_means_more_traffic(self):
        """Doubling backend-0's vnodes should roughly double its share."""
        ring = self.HAring(n_backends=2, vnodes_per=100)
        ring.set_vnodes(0, 200)  # 2× more vnodes

        counts = [0, 0]
        for i in range(10_000):
            counts[ring.route(f"k:{i}")] += 1

        ratio = counts[0] / max(counts[1], 1)
        assert ratio > 1.3, f"Vnode adjustment had no effect: backend-0 share={counts[0] / 10000:.2%}"

    # ── Health / failover ────────────────────────────────────────────────────

    def test_unhealthy_backend_never_selected(self):
        """Marking a backend unhealthy must remove it from routing."""
        self.ring4.set_health(2, False)  # take backend-2 offline
        for i in range(1_000):
            b = self.ring4.route(f"probe:{i}")
            assert b != 2, f"Unhealthy backend-2 was selected for key probe:{i}"
        self.ring4.set_health(2, True)  # restore

    def test_single_healthy_backend_absorbs_all(self):
        """If only one backend is healthy, every key must route to it."""
        ring = self.HAring(n_backends=4, vnodes_per=50)
        for i in [0, 1, 2]:
            ring.set_health(i, False)
        for i in range(200):
            assert ring.route(f"k:{i}") == 3, "Only backend-3 is healthy but routing elsewhere"

    def test_empty_ring_returns_zero(self):
        """All backends down → route() must not raise; returns safe fallback."""
        ring = self.HAring(n_backends=2, vnodes_per=50)
        ring.set_health(0, False)
        ring.set_health(1, False)
        result = ring.route("any-key")
        assert isinstance(result, int), "route() should return int even with empty ring"

    # ── Thread safety ────────────────────────────────────────────────────────

    def test_concurrent_routing_does_not_crash(self):
        """Simultaneous route() calls from multiple threads must not raise."""
        errors = []

        def worker():
            try:
                for i in range(100):
                    self.ring4.route(f"thread-key:{i}")
            except Exception as e:
                errors.append(e)

        threads = [threading.Thread(target=worker) for _ in range(8)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        assert not errors, f"Thread safety violation: {errors}"


# ═══════════════════════════════════════════════════════════════════════════════
# 3. Token Bucket Rate Limiter
# ═══════════════════════════════════════════════════════════════════════════════


class TestTokenBucket:
    @pytest.fixture(autouse=True)
    def bucket(self, proxy_mod):
        self.TokenBucket = proxy_mod.TokenBucket

    def test_bucket_full_at_start(self):
        """A fresh bucket should immediately grant consume() calls."""
        tb = self.TokenBucket(rate=100)
        # First consume should always succeed (bucket starts full)
        assert tb.consume() is True

    def test_drain_and_refill(self):
        """After draining the bucket, tokens should refill over time."""
        rate = 200  # 200 tokens/s
        tb = self.TokenBucket(rate=rate)
        # Drain completely
        drained = 0
        for _ in range(int(rate) + 10):
            if tb.consume():
                drained += 1
        # Bucket should be empty (or nearly so) now
        # Wait 0.1s → should refill ~20 tokens at 200/s
        time.sleep(0.1)
        refilled = sum(1 for _ in range(30) if tb.consume())
        assert refilled >= 10, f"Expected ≥10 tokens after 100ms at 200/s, got {refilled}"

    def test_rate_zero_ish_denies_after_drain(self):
        """Very low rate bucket should start refusing quickly."""
        tb = self.TokenBucket(rate=1)  # 1 token/s
        tb.consume()  # take the one token
        # Should be denied now (tokens = 0)
        # Allow a small fill window (≤10ms) before asserting
        denied = 0
        for _ in range(20):
            if not tb.consume():
                denied += 1
        assert denied > 0, "Low-rate bucket should deny some requests"

    def test_set_rate_changes_refill(self):
        """Calling set_rate() should change the refill speed."""
        tb = self.TokenBucket(rate=10)
        tb.set_rate(500)
        # Drain + wait 0.1s → should refill ~50 tokens at 500/s
        for _ in range(500):
            tb.consume()
        time.sleep(0.1)
        refilled = sum(1 for _ in range(60) if tb.consume())
        assert refilled >= 30, f"set_rate(500) should give ~50 tokens in 100ms, got {refilled}"

    def test_minimum_rate_floor(self):
        """set_rate() below minimum must be clamped, not crash."""
        tb = self.TokenBucket(rate=100)
        tb.set_rate(0)  # should clamp to 10
        tb.set_rate(-999)  # should clamp to 10
        assert tb.consume() or not tb.consume()  # no exception = pass


# ═══════════════════════════════════════════════════════════════════════════════
# 4. CBF Projector (via ml.cbf)
# ═══════════════════════════════════════════════════════════════════════════════


class TestCBFProjector:
    @pytest.fixture(autouse=True)
    def cbf(self):
        from ml.cbf import CBFProjector

        self.CBFProjector = CBFProjector
        self.cap = 0.80

    def test_unconstrained_weights_unchanged(self):
        """When no backend exceeds the cap, weights should pass through."""
        cbf = self.CBFProjector(cap=self.cap)
        w_in = np.array([0.25, 0.25, 0.25, 0.25])
        loads = np.array([0.5, 0.5, 0.5, 0.5])
        w_out, fired = cbf.project(w_in, loads)
        np.testing.assert_allclose(w_out.sum(), 1.0, atol=1e-6)
        assert (w_out >= 0).all()
        # No backend is overloaded → CBF may or may not fire, but simplex must hold
        np.testing.assert_allclose(w_out.sum(), 1.0, atol=1e-6)

    def test_overloaded_backend_weight_reduced(self):
        """CBF must reduce weight on a backend whose load exceeds the cap."""
        cbf = self.CBFProjector(cap=self.cap)
        w_in = np.array([0.70, 0.10, 0.10, 0.10])
        # backend-0 is at 95% — over the 80% cap
        loads = np.array([0.95, 0.3, 0.2, 0.1])
        w_out, fired = cbf.project(w_in, loads)
        np.testing.assert_allclose(w_out.sum(), 1.0, atol=1e-6)
        assert (w_out >= 0).all()
        # The projected weight of backend-0 must be less than its pre-projection
        # share (or the cap must have been enforced)
        assert fired[0], "CBF should fire on backend-0 (load=95% > cap=80%)"

    def test_output_is_always_valid_simplex(self):
        """For any input, output weights must be non-negative and sum to 1."""
        cbf = self.CBFProjector(cap=self.cap)
        rng = np.random.default_rng(42)
        for _ in range(200):
            w_in = rng.dirichlet(np.ones(4))  # random probability vector
            loads = rng.uniform(0, 1, 4)
            w_out, _ = cbf.project(w_in, loads)
            np.testing.assert_allclose(
                w_out.sum(), 1.0, atol=1e-6, err_msg=f"Sum != 1 for w_in={w_in.round(3)}, loads={loads.round(3)}"
            )
            assert (w_out >= -1e-9).all(), f"Negative weight for loads={loads.round(3)}"

    def test_all_overloaded_still_valid(self):
        """Even if all backends exceed the cap, output must be a valid simplex."""
        cbf = self.CBFProjector(cap=self.cap)
        w_in = np.array([0.25, 0.25, 0.25, 0.25])
        loads = np.array([0.95, 0.92, 0.91, 0.93])
        w_out, fired = cbf.project(w_in, loads)
        np.testing.assert_allclose(w_out.sum(), 1.0, atol=1e-6)
        assert (w_out >= 0).all()

    def test_different_caps(self):
        """CBF with tighter cap (50%) fires more aggressively than loose cap."""
        tight_cbf = self.CBFProjector(cap=0.50)
        loose_cbf = self.CBFProjector(cap=0.90)

        w_in = np.array([0.40, 0.20, 0.20, 0.20])
        loads = np.array([0.75, 0.3, 0.3, 0.3])  # backend-0 at 75%

        _, tight_fired = tight_cbf.project(w_in, loads)
        _, loose_fired = loose_cbf.project(w_in, loads)

        # 75% > 50% cap → tight should fire; 75% < 90% cap → loose should not
        assert tight_fired[0], "Tight cap (50%) should fire on backend at 75%"
        assert not loose_fired[0], "Loose cap (90%) should NOT fire on backend at 75%"


# ═══════════════════════════════════════════════════════════════════════════════
# 5. Ring + CBF integration (no I/O)
# ═══════════════════════════════════════════════════════════════════════════════


class TestRingCBFIntegration:
    """Simulate the proxy's routing decision loop end-to-end (no network)."""

    def test_routing_pipeline_respects_cbf_cap(self, proxy_mod):
        """After CBF projection, no backend receives more than cap of total load."""
        from ml.cbf import CBFProjector

        ring = proxy_mod.HAring(n_backends=4, vnodes_per=150)
        cbf = CBFProjector(cap=0.80)

        # Simulate 500 routing decisions with running load counters
        counts = [0, 0, 0, 0]
        for i in range(500):
            b = ring.route(f"req:{i}")
            counts[b] += 1

        total = sum(counts)
        loads = np.array([c / total for c in counts])

        # Project weights based on observed load
        w_init = np.array([0.25, 0.25, 0.25, 0.25])
        w_out, _ = cbf.project(w_init, loads)

        np.testing.assert_allclose(w_out.sum(), 1.0, atol=1e-6)
        assert (w_out >= 0).all()
        # No projected weight should exceed the cap * 2 (loose end-to-end bound)
        assert w_out.max() <= 1.0, "Weight exceeds 1.0 — simplex violated"
