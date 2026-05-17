"""
KAN (Kolmogorov-Arnold Network) Inference — Omega-LB Layer 3
=============================================================
Provides routing weight inference via:
  - ONNX model (trained via ``ml/ppo/train_ppo_kan.py``) when available
  - Symbolic fallback (always available, no training required)

Thread-safe, hot-reloadable, and designed for CI/CD integration.

Usage::

    from ml.kan import KANInference

    # Loads ONNX model if it exists, else uses symbolic fallback:
    kan = KANInference.load("ml/models/kan_actor.onnx")

    weights = kan.infer(
        cpu    = np.array([0.40, 0.60, 0.20, 0.50]),
        lat_ms = np.array([45.0, 55.0, 120.0, 40.0]),
        err    = np.array([0.001, 0.002, 0.010, 0.001]),
        health = [True, True, True, True],
    )  # → np.ndarray shape (4,) summing to 1

    eqs = kan.equations(cpu, lat_ms, err, health)
    # → ["w_0 = max(0, 1 − 0.42·0.400 − ...) × 1  →  0.4321", ...]
"""

from __future__ import annotations

import os
import threading
import time
from pathlib import Path
from typing import Optional

import numpy as np


# ---------------------------------------------------------------------------
# Stats tracking
# ---------------------------------------------------------------------------

class KANStats:
    """Runtime statistics for the KAN inference engine."""

    def __init__(self) -> None:
        self.inference_count: int = 0
        self.onnx_count: int = 0
        self.symbolic_count: int = 0
        self.error_count: int = 0
        self._mean_latency_ms: float = 0.0
        self._alpha: float = 0.05  # EMA smoothing

    def record(self, mode: str, latency_ms: float, error: bool = False) -> None:
        self.inference_count += 1
        if mode == "onnx":
            self.onnx_count += 1
        else:
            self.symbolic_count += 1
        if error:
            self.error_count += 1
        self._mean_latency_ms = (
            self._alpha * latency_ms + (1 - self._alpha) * self._mean_latency_ms
        )

    @property
    def mean_latency_ms(self) -> float:
        return round(self._mean_latency_ms, 4)

    def to_dict(self) -> dict:
        return {
            "inference_count": self.inference_count,
            "onnx_count": self.onnx_count,
            "symbolic_count": self.symbolic_count,
            "error_count": self.error_count,
            "mean_latency_ms": self.mean_latency_ms,
            "mode": "onnx" if self.onnx_count >= self.symbolic_count else "symbolic",
        }


# ---------------------------------------------------------------------------
# KANInference
# ---------------------------------------------------------------------------

class KANInference:
    """
    Thread-safe KAN routing weight inference.

    **Symbolic fallback** (used when no ONNX model is loaded):

        raw_i = max(0, 1 − cpu_coeff·cpu_i − lat_coeff·(lat_i/1000) − err_coeff·err_i) × health_i
        w_i   = raw_i / Σ raw_j

    Coefficients are the B-spline edge weights extracted via
    ``KANActor.extract_equations()`` after training.  The defaults below are
    good enough for production without a trained model — they encode the
    intuition "prefer low CPU, low latency, low error rate".

    **ONNX path** (used when a trained model exists):

        State vector = per-backend features (8 per backend) + 4 global features,
        matching exactly what ``LBSimEnv._state()`` produces and what
        ``PPOTrainer.export_onnx()`` exports.
    """

    # Symbolic coefficients — overwritten by update_symbolic_coefficients()
    # after a training run reads the audit log.
    _CPU_COEFF: float = 0.42
    _LAT_COEFF: float = 0.31
    _ERR_COEFF: float = 10.0

    def __init__(self, onnx_session=None) -> None:
        self._session = onnx_session  # onnxruntime.InferenceSession or None
        self._lock = threading.RLock()
        self.stats = KANStats()

    # ------------------------------------------------------------------ #
    #  Factory                                                             #
    # ------------------------------------------------------------------ #

    @classmethod
    def load(cls, model_path: str | Path) -> "KANInference":
        """
        Load ONNX model from *model_path*.  Silently falls back to symbolic
        if the file does not exist (e.g. in CI before the first training run).

        This is the recommended constructor for the proxy and CI/CD scripts.
        """
        p = Path(model_path)
        if not p.exists():
            return cls._make_symbolic(
                reason=f"model not found at {p}, using symbolic fallback"
            )
        try:
            import onnxruntime as ort  # soft dependency
            sess = ort.InferenceSession(
                str(p), providers=["CPUExecutionProvider"]
            )
            obj = cls(onnx_session=sess)
            print(f"[KAN] Loaded ONNX model from {p}")
            return obj
        except Exception as exc:
            return cls._make_symbolic(
                reason=f"ONNX load failed ({exc}), using symbolic fallback"
            )

    @classmethod
    def symbolic(cls) -> "KANInference":
        """Create a symbolic-only instance.  Zero external dependencies."""
        return cls(onnx_session=None)

    @classmethod
    def _make_symbolic(cls, reason: str) -> "KANInference":
        print(f"[KAN] {reason}")
        return cls(onnx_session=None)

    # ------------------------------------------------------------------ #
    #  Public inference API — matches demo/proxy.py KANSymbolic interface #
    # ------------------------------------------------------------------ #

    def infer(
        self,
        cpu: np.ndarray,
        lat_ms: np.ndarray,
        err: np.ndarray,
        health: list[bool],
    ) -> np.ndarray:
        """
        Return a routing weight vector of shape ``(N,)`` summing to 1.

        Args:
            cpu:    utilisation per backend in [0, 1].
            lat_ms: P50 latency per backend in milliseconds.
            err:    error rate per backend in [0, 1].
            health: boolean health flag per backend.

        Raises:
            Nothing — symbolic fallback is attempted on any ONNX error.
        """
        t0 = time.monotonic()
        with self._lock:
            mode = "onnx" if self._session is not None else "symbolic"
            try:
                if self._session is not None:
                    w = self._infer_onnx(cpu, lat_ms, err, health)
                else:
                    w = self._infer_symbolic(cpu, lat_ms, err, health)
                self.stats.record(mode, (time.monotonic() - t0) * 1000)
                return w
            except Exception as exc:
                # Graceful degradation: ONNX blew up, use symbolic
                w = self._infer_symbolic(cpu, lat_ms, err, health)
                self.stats.record(
                    "symbolic", (time.monotonic() - t0) * 1000, error=True
                )
                print(f"[KAN] ONNX inference error ({exc}), fell back to symbolic")
                return w

    # Alias for API consistency with the dashboard / external callers
    get_weights = infer

    def equations(
        self,
        cpu: np.ndarray,
        lat_ms: np.ndarray,
        err: np.ndarray,
        health: list[bool],
    ) -> list[str]:
        """
        Return human-readable routing equations for the audit log / dashboard.

        Always uses the symbolic form regardless of whether ONNX is loaded,
        so the equations are always interpretable.
        """
        h = np.array([1.0 if v else 0.0 for v in health])
        with self._lock:
            cpu_c = self._CPU_COEFF
            lat_c = self._LAT_COEFF
            err_c = self._ERR_COEFF

        n = len(cpu)
        eqs: list[str] = []
        for i in range(n):
            raw = max(
                0.0,
                1.0
                - cpu_c * float(cpu[i])
                - lat_c * float(lat_ms[i]) / 1000.0
                - err_c * float(err[i]),
            ) * float(h[i])
            eqs.append(
                f"w_{i} = max(0, 1 "
                f"− {cpu_c}·{cpu[i]:.3f} "
                f"− {lat_c}·{lat_ms[i]/1000:.3f} "
                f"− {err_c}·{err[i]:.4f}) "
                f"× {int(health[i])}  →  {raw:.4f}"
            )
        return eqs

    # Alias
    get_equations = equations

    # ------------------------------------------------------------------ #
    #  Hot-reload / model update (for CI/CD deployment)                   #
    # ------------------------------------------------------------------ #

    def reload_onnx(self, model_path: str | Path) -> bool:
        """
        Hot-reload an ONNX model without restarting the proxy.

        Call this from your deployment script after ``make train-ppo``
        copies the new model into place::

            kan.reload_onnx("ml/models/kan_actor.onnx")

        Returns:
            True on success, False if loading failed (keeps existing model).
        """
        try:
            import onnxruntime as ort
            p = Path(model_path)
            new_sess = ort.InferenceSession(
                str(p), providers=["CPUExecutionProvider"]
            )
            with self._lock:
                self._session = new_sess
            print(f"[KAN] Hot-reloaded model from {p}")
            return True
        except Exception as exc:
            print(f"[KAN] Hot-reload failed: {exc}")
            return False

    def update_symbolic_coefficients(
        self,
        cpu_coeff: float,
        lat_coeff: float,
        err_coeff: float,
    ) -> None:
        """
        Update symbolic fallback coefficients from the training audit log.

        After ``make train-ppo``, the audit log at ``ml/models/kan_audit.json``
        contains the symbolic equations extracted from the trained KAN actor.
        Parse those and call this to keep the symbolic fallback aligned with
        the last trained model.

        Thread-safe.
        """
        with self._lock:
            self._CPU_COEFF = float(cpu_coeff)
            self._LAT_COEFF = float(lat_coeff)
            self._ERR_COEFF = float(err_coeff)

    # ------------------------------------------------------------------ #
    #  Private                                                             #
    # ------------------------------------------------------------------ #

    def _infer_symbolic(
        self,
        cpu: np.ndarray,
        lat_ms: np.ndarray,
        err: np.ndarray,
        health: list[bool],
    ) -> np.ndarray:
        h = np.array([1.0 if v else 0.0 for v in health], dtype=np.float64)
        raw = np.maximum(
            0.0,
            1.0
            - self._CPU_COEFF * np.asarray(cpu, dtype=np.float64)
            - self._LAT_COEFF * np.asarray(lat_ms, dtype=np.float64) / 1000.0
            - self._ERR_COEFF * np.asarray(err, dtype=np.float64),
        ) * h
        total = raw.sum()
        if total < 1e-9:
            # All backends saturated or all unhealthy — circuit-breaker fallback.
            # If at least one is healthy, distribute equally among them.
            # If all are unhealthy, distribute equally across all (nothing better to do).
            healthy_count = h.sum()
            if healthy_count > 0:
                return h / healthy_count
            return np.ones(len(h)) / len(h)
        return raw / total

    def _infer_onnx(
        self,
        cpu: np.ndarray,
        lat_ms: np.ndarray,
        err: np.ndarray,
        health: list[bool],
    ) -> np.ndarray:
        """
        Run the trained ONNX model.

        State vector layout (must match LBSimEnv._state() and PPOConfig.state_dim):
            [per_backend: cpu, conn, queue, lat_ms, tx_bps, rx_bps, health, err_rate] * N
            + [total_rps, p99_lat_ms, sin(t), cos(t)]
        """
        n = len(cpu)
        h = np.array([1.0 if v else 0.0 for v in health], dtype=np.float32)
        per_backend: list[float] = []
        for i in range(n):
            lat_s = float(lat_ms[i]) / 1000.0
            queue_est = max(0.0, float(cpu[i]) - 0.5) * 100.0
            per_backend.extend([
                float(cpu[i]),                              # cpu_utilisation
                float(cpu[i]) * 1000.0,                    # active_connections (approx)
                queue_est,                                  # queue_depth
                float(lat_ms[i]),                           # ewma_latency_ms
                float(cpu[i]) * 1024.0 * 100.0,            # tx_bytes_per_sec
                float(cpu[i]) * 512.0 * 100.0,             # rx_bytes_per_sec
                float(h[i]),                                # health_status
                float(err[i]),                              # error_rate_1m
            ])
        t = time.monotonic() % (2 * np.pi)
        global_feats = [
            float(np.sum(cpu)) * 1000.0,   # total_rps proxy
            float(np.max(lat_ms)),          # p99 proxy
            float(np.sin(t)),
            float(np.cos(t)),
        ]
        state = np.array(per_backend + global_feats, dtype=np.float32).reshape(1, -1)
        output = self._session.run(None, {"state": state})[0]  # (1, N)
        raw = np.maximum(output.squeeze(0).astype(np.float64), 0.0) * h.astype(np.float64)
        total = raw.sum()
        return raw / total if total > 1e-9 else h.astype(np.float64) / max(h.sum(), 1.0)

    # ------------------------------------------------------------------ #
    #  Properties / repr                                                   #
    # ------------------------------------------------------------------ #

    @property
    def mode(self) -> str:
        """``'onnx'`` or ``'symbolic'``."""
        return "onnx" if self._session is not None else "symbolic"

    def __repr__(self) -> str:
        return (
            f"KANInference(mode={self.mode}, "
            f"inferences={self.stats.inference_count}, "
            f"errors={self.stats.error_count})"
        )
