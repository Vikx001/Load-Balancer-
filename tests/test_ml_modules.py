"""
Omega-LB ML Module Tests
Tests the importable ml.kan, ml.cbf, and ml.simulation packages.
Run: python -m pytest tests/test_ml_modules.py -v
"""
import sys
import os
import math
import unittest

import numpy as np

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


# ─── ml.kan: KANInference ─────────────────────────────────────────────────────

class TestKANInferenceSymbolic(unittest.TestCase):

    def setUp(self):
        from ml.kan import KANInference
        self.kan = KANInference.symbolic()

    # ── Output invariants ────────────────────────────────────────────────

    def test_output_sums_to_one(self):
        w = self._infer([0.4, 0.6, 0.2, 0.5], [45, 55, 120, 40],
                        [0.001, 0.002, 0.01, 0.001], [True]*4)
        self.assertAlmostEqual(float(w.sum()), 1.0, places=6)

    def test_output_all_nonnegative(self):
        w = self._infer([0.4, 0.6, 0.2, 0.5], [45, 55, 120, 40],
                        [0.001, 0.002, 0.01, 0.001], [True]*4)
        self.assertTrue((w >= 0).all(), f"negative weights: {w}")

    def test_output_shape(self):
        w = self._infer([0.1, 0.2, 0.3], [30, 40, 50],
                        [0.0, 0.0, 0.0], [True]*3)
        self.assertEqual(w.shape, (3,))

    # ── Health masking ───────────────────────────────────────────────────

    def test_unhealthy_backend_gets_zero_weight(self):
        w = self._infer([0.1, 0.1, 0.1, 0.1], [30, 30, 30, 30],
                        [0.0]*4, [True, False, True, True])
        self.assertAlmostEqual(float(w[1]), 0.0, places=6)

    def test_all_unhealthy_equal_fallback(self):
        """When all backends are down, get equal shares (avoids div-by-zero)."""
        w = self._infer([0.9]*4, [900]*4, [1.0]*4, [False]*4)
        self.assertAlmostEqual(float(w.sum()), 1.0, places=6)

    # ── Saturation handling ──────────────────────────────────────────────

    def test_all_saturated_falls_back_gracefully(self):
        """All backends at 100% CPU + high error → should not crash."""
        w = self._infer([1.0]*4, [2000]*4, [1.0]*4, [True]*4)
        self.assertAlmostEqual(float(w.sum()), 1.0, places=6)

    # ── Policy direction ─────────────────────────────────────────────────

    def test_low_cpu_gets_more_weight(self):
        """Backend with lower CPU should receive higher weight, all else equal."""
        w = self._infer([0.2, 0.8], [50, 50], [0.001, 0.001], [True, True])
        self.assertGreater(float(w[0]), float(w[1]),
                           "low-CPU backend should get more weight")

    def test_high_error_rate_reduces_weight(self):
        w = self._infer([0.3, 0.3], [50, 50], [0.001, 0.1], [True, True])
        self.assertGreater(float(w[0]), float(w[1]),
                           "high error rate backend should get less weight")

    def test_high_latency_reduces_weight(self):
        w = self._infer([0.3, 0.3], [50, 500], [0.001, 0.001], [True, True])
        self.assertGreater(float(w[0]), float(w[1]),
                           "high latency backend should get less weight")

    # ── Equations ────────────────────────────────────────────────────────

    def test_equations_count(self):
        eqs = self.kan.equations(
            np.array([0.3, 0.5]), np.array([40., 60.]),
            np.array([0.001, 0.002]), [True, True]
        )
        self.assertEqual(len(eqs), 2)

    def test_equations_format(self):
        eqs = self.kan.equations(
            np.array([0.3]), np.array([40.]), np.array([0.001]), [True]
        )
        self.assertIn("w_0", eqs[0])
        self.assertIn("→", eqs[0])

    # ── Stats ────────────────────────────────────────────────────────────

    def test_stats_recorded(self):
        for _ in range(5):
            self._infer([0.3]*3, [40.]*3, [0.001]*3, [True]*3)
        self.assertEqual(self.kan.stats.inference_count, 5)
        self.assertEqual(self.kan.stats.symbolic_count, 5)

    # ── Coefficient update ───────────────────────────────────────────────

    def test_coefficient_update(self):
        self.kan.update_symbolic_coefficients(0.5, 0.4, 15.0)
        self.assertAlmostEqual(self.kan._CPU_COEFF, 0.5)
        self.assertAlmostEqual(self.kan._LAT_COEFF, 0.4)
        self.assertAlmostEqual(self.kan._ERR_COEFF, 15.0)

    # ── Mode property ────────────────────────────────────────────────────

    def test_mode_is_symbolic(self):
        self.assertEqual(self.kan.mode, "symbolic")

    # ── ONNX load fallback ───────────────────────────────────────────────

    def test_load_missing_file_returns_symbolic(self):
        from ml.kan import KANInference
        kan = KANInference.load("/nonexistent/model.onnx")
        self.assertEqual(kan.mode, "symbolic")

    # ── Helper ───────────────────────────────────────────────────────────

    def _infer(self, cpu, lat_ms, err, health):
        return self.kan.infer(
            np.array(cpu, dtype=np.float64),
            np.array(lat_ms, dtype=np.float64),
            np.array(err, dtype=np.float64),
            health,
        )


# ─── ml.cbf: CBFProjector ─────────────────────────────────────────────────────

class TestCBFProjector(unittest.TestCase):

    def setUp(self):
        from ml.cbf import CBFProjector
        self.cbf = CBFProjector(cap=0.80, lam=0.5, lr=0.01, max_iter=100)

    # ── Simplex invariants ───────────────────────────────────────────────

    def test_output_sums_to_one(self):
        w, _ = self.cbf.project(
            np.array([0.4, 0.3, 0.2, 0.1]),
            np.array([0.5, 0.9, 0.3, 0.6])
        )
        self.assertAlmostEqual(float(w.sum()), 1.0, places=5)

    def test_output_all_nonnegative(self):
        w, _ = self.cbf.project(
            np.array([0.25]*4),
            np.array([0.7]*4)
        )
        self.assertTrue((w >= -1e-9).all(), f"negative weights: {w}")

    # ── Safety enforcement ───────────────────────────────────────────────

    def test_overloaded_backend_weight_reduced(self):
        """Backend with load > cap should have weight reduced after projection."""
        raw = np.array([0.9, 0.03, 0.03, 0.04])
        loads = np.array([0.85, 0.1, 0.1, 0.1])
        w, fired = self.cbf.project(raw, loads)
        raw_norm = raw / raw.sum()
        self.assertLess(float(w[0]), float(raw_norm[0]),
                        "CBF should reduce weight on overloaded backend")

    def test_cbf_fires_on_overloaded_backend(self):
        _, fired = self.cbf.project(
            np.array([0.9, 0.05, 0.03, 0.02]),
            np.array([0.95, 0.2, 0.2, 0.2])
        )
        self.assertTrue(fired[0], "CBF should fire on backend-0 (load=0.95 > cap=0.80)")

    def test_safe_loads_leave_weights_unchanged(self):
        """If all loads are well below cap, projection should barely change weights."""
        raw = np.array([0.4, 0.3, 0.2, 0.1])
        loads = np.array([0.1, 0.1, 0.1, 0.1])
        w, fired = self.cbf.project(raw, loads)
        raw_norm = raw / raw.sum()
        np.testing.assert_allclose(w, raw_norm, atol=0.05,
                                   err_msg="Safe loads: weights should not change much")
        self.assertFalse(any(fired), "No CBF should fire when loads are safe")

    def test_cbf_does_not_fire_when_safe(self):
        _, fired = self.cbf.project(
            np.array([0.25]*4),
            np.array([0.2, 0.3, 0.1, 0.4])
        )
        self.assertFalse(any(fired))

    # ── Per-backend caps ─────────────────────────────────────────────────

    def test_per_backend_caps_respected(self):
        """Backend with tighter cap should see CBF fire at lower load."""
        caps = np.array([0.50, 0.80, 0.80, 0.80])
        loads = np.array([0.55, 0.3, 0.3, 0.3])  # only backend-0 over its cap
        _, fired = self.cbf.project(
            np.array([0.4, 0.2, 0.2, 0.2]), loads, caps=caps
        )
        self.assertTrue(fired[0])
        self.assertFalse(fired[1])

    # ── Simplex projection static method ────────────────────────────────

    def test_project_simplex_sums_to_one(self):
        v = np.array([2.0, -1.0, 0.5, 3.0])
        w = self.cbf._project_simplex(v)
        self.assertAlmostEqual(float(w.sum()), 1.0, places=6)

    def test_project_simplex_all_nonneg(self):
        v = np.array([-3.0, -2.0, -1.0, -0.5])
        w = self.cbf._project_simplex(v)
        self.assertTrue((w >= 0).all())

    def test_project_simplex_idempotent(self):
        """Projecting a simplex vector is a no-op."""
        v = np.array([0.4, 0.3, 0.2, 0.1])
        w = self.cbf._project_simplex(v)
        np.testing.assert_allclose(w, v, atol=1e-6)

    # ── Detailed result ──────────────────────────────────────────────────

    def test_detailed_result_fields(self):
        from ml.cbf import CBFProjector
        result = self.cbf.project_detailed(
            np.array([0.7, 0.1, 0.1, 0.1]),
            np.array([0.9, 0.3, 0.3, 0.3])
        )
        self.assertIsInstance(result.weights, np.ndarray)
        self.assertIsInstance(result.fired, list)
        self.assertIsInstance(result.iterations, int)
        self.assertIsInstance(result.converged, bool)


# ─── ml.cbf: SafetyMonitor ───────────────────────────────────────────────────

class TestSafetyMonitor(unittest.TestCase):

    def setUp(self):
        from ml.cbf import SafetyMonitor
        self.monitor = SafetyMonitor(n_backends=4, cap=0.80, history_len=20)

    def test_step_returns_valid_simplex(self):
        w, fired = self.monitor.step(
            np.array([0.4, 0.3, 0.2, 0.1]),
            np.array([0.5, 0.3, 0.2, 0.4])
        )
        self.assertAlmostEqual(float(w.sum()), 1.0, places=5)

    def test_violation_rate_increases_under_overload(self):
        for _ in range(10):
            self.monitor.step(
                np.array([0.8, 0.1, 0.05, 0.05]),
                np.array([0.9, 0.2, 0.2, 0.2])
            )
        rate = self.monitor.violation_rate(0)
        self.assertGreater(rate, 0.0, "Should have detected violations on backend-0")

    def test_violation_rate_zero_when_safe(self):
        for _ in range(10):
            self.monitor.step(
                np.array([0.25]*4),
                np.array([0.1, 0.2, 0.15, 0.1])
            )
        for i in range(4):
            self.assertAlmostEqual(self.monitor.violation_rate(i), 0.0, places=6)

    def test_audit_dict_structure(self):
        self.monitor.step(
            np.array([0.25]*4),
            np.array([0.1]*4)
        )
        audit = self.monitor.audit()
        required_keys = {
            "total_projections", "total_violations",
            "mean_iters_per_step", "violation_rates", "cap"
        }
        self.assertTrue(required_keys.issubset(audit.keys()),
                        f"Missing keys: {required_keys - audit.keys()}")
        self.assertEqual(len(audit["violation_rates"]), 4)

    def test_total_projections_increments(self):
        for i in range(7):
            self.monitor.step(np.array([0.25]*4), np.array([0.2]*4))
        self.assertEqual(self.monitor.total_projections, 7)

    def test_recent_violations_returns_list(self):
        # Trigger some violations
        for _ in range(5):
            self.monitor.step(
                np.array([0.9, 0.04, 0.03, 0.03]),
                np.array([0.95, 0.2, 0.2, 0.2])
            )
        violations = self.monitor.recent_violations(10)
        self.assertIsInstance(violations, list)
        if violations:
            v = violations[0]
            self.assertIn("backend", v)
            self.assertIn("load", v)
            self.assertIn("cap", v)


# ─── ml.simulation: LBSimEnv ──────────────────────────────────────────────────

class TestLBSimEnv(unittest.TestCase):

    def setUp(self):
        from ml.simulation import LBSimEnv
        self.env = LBSimEnv(n_backends=4)

    def test_state_dim(self):
        state, _ = self.env.reset()
        self.assertEqual(len(state), 4 * 8 + 4)

    def test_state_dtype_float32(self):
        state, _ = self.env.reset()
        self.assertEqual(state.dtype, np.float32)

    def test_step_returns_correct_shapes(self):
        self.env.reset()
        w = np.array([0.25, 0.25, 0.25, 0.25])
        state, reward, terminated, truncated, info = self.env.step(w)
        self.assertEqual(len(state), 4 * 8 + 4)
        self.assertIsInstance(reward, float)
        self.assertIsInstance(terminated, bool)
        self.assertIsInstance(truncated, bool)

    def test_reset_gives_different_states(self):
        s1, _ = self.env.reset(seed=1)
        s2, _ = self.env.reset(seed=99)
        self.assertFalse(np.allclose(s1, s2),
                         "Different seeds should give different initial states")

    def test_episode_terminates(self):
        self.env.reset()
        done = False
        for _ in range(3000):
            _, _, terminated, truncated, _ = self.env.step(
                np.array([0.25]*4)
            )
            if terminated or truncated:
                done = True
                break
        self.assertTrue(done, "Episode should terminate within 3000 steps")

    def test_step_compat_three_tuple(self):
        self.env.reset()
        state, reward, done = self.env.step_compat(np.array([0.25]*4))
        self.assertIsInstance(done, bool)

    def test_equal_weights_distribute_load(self):
        """Equal weights over many steps should keep loads roughly balanced."""
        self.env.reset(seed=42)
        w = np.array([0.25, 0.25, 0.25, 0.25])
        for _ in range(100):
            state, _, terminated, truncated, _ = self.env.step(w)
            if terminated or truncated:
                break
        cpu = self.env.loads / self.env.capacities
        max_imbalance = float(np.max(cpu) - np.min(cpu))
        self.assertLess(max_imbalance, 0.5,
                        f"Equal weights produced imbalanced loads: {cpu}")

    def test_concentrated_weights_increase_overloaded_backend(self):
        """Routing all traffic to one backend should overload it."""
        self.env.reset(seed=42)
        w_all_0 = np.array([1.0, 0.0, 0.0, 0.0])
        for _ in range(200):
            self.env.step(w_all_0)
        cpu = self.env.loads / self.env.capacities
        self.assertGreater(float(cpu[0]), float(cpu[1]),
                           "Backend-0 should be more loaded than others")

    def test_info_dict_keys(self):
        self.env.reset()
        _, _, _, _, info = self.env.step(np.array([0.25]*4))
        for key in ("total_rps", "p99_ms", "cpu", "violations"):
            self.assertIn(key, info)

    def test_state_dim_property(self):
        from ml.simulation import LBSimEnv
        env = LBSimEnv(n_backends=8)
        self.assertEqual(env.state_dim, 8 * 8 + 4)
        self.assertEqual(env.action_dim, 8)

    def test_sim_config_state_dim(self):
        from ml.simulation import SimConfig
        cfg = SimConfig(num_backends=6)
        self.assertEqual(cfg.state_dim, 6 * 8 + 4)


# ─── Integration: KAN + CBF pipeline ─────────────────────────────────────────

class TestKANCBFPipeline(unittest.TestCase):
    """
    End-to-end test of the Layer 2+3 pipeline:
        KANInference.infer() → CBFProjector.project()
    Mirrors what the proxy's control loop does every 500ms.
    """

    def setUp(self):
        from ml.kan import KANInference
        from ml.cbf import SafetyMonitor
        self.kan = KANInference.symbolic()
        self.monitor = SafetyMonitor(n_backends=4, cap=0.80)

    def _pipeline_step(self, cpu, lat_ms, err, health, loads):
        cpu    = np.array(cpu, dtype=np.float64)
        lat_ms = np.array(lat_ms, dtype=np.float64)
        err    = np.array(err, dtype=np.float64)
        loads  = np.array(loads, dtype=np.float64)

        weights = self.kan.infer(cpu, lat_ms, err, health)
        safe_w, fired = self.monitor.step(weights, loads)
        return safe_w, fired

    def test_pipeline_output_is_valid_simplex(self):
        w, _ = self._pipeline_step(
            [0.4, 0.6, 0.2, 0.5], [45, 55, 120, 40],
            [0.001, 0.002, 0.01, 0.001], [True]*4,
            [0.5, 0.9, 0.3, 0.6]
        )
        self.assertAlmostEqual(float(w.sum()), 1.0, places=5)
        self.assertTrue((w >= 0).all())

    def test_pipeline_fires_on_overload(self):
        # Backend-1 severely overloaded
        _, fired = self._pipeline_step(
            [0.3, 0.95, 0.3, 0.3], [40, 200, 40, 40],
            [0.001, 0.05, 0.001, 0.001], [True]*4,
            [0.2, 0.95, 0.2, 0.2]
        )
        self.assertTrue(fired[1], "CBF should fire on severely overloaded backend-1")

    def test_pipeline_handles_single_healthy_backend(self):
        """One healthy backend should receive all traffic."""
        w, _ = self._pipeline_step(
            [0.3]*4, [50]*4, [0.001]*4,
            [False, True, False, False],
            [0.1]*4
        )
        self.assertAlmostEqual(float(w.sum()), 1.0, places=5)
        self.assertAlmostEqual(float(w[1]), 1.0, places=5)


if __name__ == "__main__":
    unittest.main(verbosity=2)
