"""
CBF (Control Barrier Function) Safety Projection — Omega-LB Layer 2
====================================================================
Ensures no backend exceeds its capacity cap at the routing-weight level.

Safety invariant::

    h_i(w) = cap_i − load_i ≥ 0   ∀ i

When the invariant is violated by the raw KAN weights, the projector
applies a gradient correction and re-projects onto the probability simplex.

Usage::

    from ml.cbf import CBFProjector, SafetyMonitor

    cbf = CBFProjector(cap=0.80, lam=0.5)
    safe_weights, cbf_fired = cbf.project(raw_weights, current_loads)

    monitor = SafetyMonitor(n_backends=4, cap=0.80)
    safe_weights, cbf_fired = monitor.step(raw_weights, current_loads)
    print(monitor.audit())
"""

from __future__ import annotations

import threading
import time
from collections import deque
from dataclasses import dataclass, field
from typing import NamedTuple

import numpy as np


# ---------------------------------------------------------------------------
# Result / violation types
# ---------------------------------------------------------------------------

class CBFResult(NamedTuple):
    """Detailed result from a CBF projection step."""
    weights: np.ndarray    # projected weights, sums to 1
    fired: list[bool]      # per-backend: True if CBF was active
    iterations: int        # gradient descent iterations used
    converged: bool        # True if all constraints satisfied before max_iter


@dataclass
class CBFViolation:
    timestamp: float
    backend_id: int
    load: float
    cap: float
    weight_before: float
    weight_after: float


# ---------------------------------------------------------------------------
# CBFProjector — pure numpy, no torch required
# ---------------------------------------------------------------------------

class CBFProjector:
    """
    Gradient-descent CBF projection onto safety ∩ simplex.

    Algorithm
    ---------
    For each iteration:

    1. Compute barrier value: ``h_i = cap_i − load_i``
    2. For violated backends (``h_i < 0``): add a correction gradient
       ``∂/∂w_i = lam · |h_i|`` (pushes weight away from the overloaded node)
    3. Gradient step: ``w ← w − lr · grad``
    4. Project onto probability simplex (Duchi et al. O(n log n) algorithm)
    5. Repeat until all constraints satisfied or ``max_iter`` reached.

    This is the **runtime** version (pure numpy, no autograd).
    The training version with PyTorch autograd lives in
    ``ml/ppo/train_ppo_kan.py:cbf_project()``.
    """

    def __init__(
        self,
        cap: float = 0.80,
        lam: float = 0.5,
        lr: float = 0.01,
        max_iter: int = 100,
        tol: float = 1e-6,
    ) -> None:
        """
        Args:
            cap:      default per-backend capacity cap in [0, 1].
            lam:      CBF correction strength.  Higher ⟹ more aggressive.
            lr:       gradient descent step size.
            max_iter: iteration budget per call.
            tol:      convergence tolerance on max weight change.
        """
        self.cap = cap
        self.lam = lam
        self.lr = lr
        self.max_iter = max_iter
        self.tol = tol

    def project(
        self,
        weights: np.ndarray,
        loads: np.ndarray,
        caps: np.ndarray | None = None,
    ) -> tuple[np.ndarray, list[bool]]:
        """
        Project *weights* onto the safe region and return a 2-tuple.

        Args:
            weights: raw routing weights (any non-negative vector).
            loads:   current utilisation per backend in [0, 1].
            caps:    optional per-backend caps; falls back to ``self.cap``.

        Returns:
            (safe_weights, cbf_fired) where *safe_weights* sums to 1 and
            *cbf_fired[i]* is True if backend *i* violated its cap.
        """
        result = self._project_full(weights, loads, caps)
        return result.weights, result.fired

    def project_detailed(
        self,
        weights: np.ndarray,
        loads: np.ndarray,
        caps: np.ndarray | None = None,
    ) -> CBFResult:
        """Same as :meth:`project` but returns full :class:`CBFResult`."""
        return self._project_full(weights, loads, caps)

    # ------------------------------------------------------------------ #
    #  Core algorithm                                                      #
    # ------------------------------------------------------------------ #

    def _project_full(
        self,
        weights: np.ndarray,
        loads: np.ndarray,
        caps: np.ndarray | None,
    ) -> CBFResult:
        n = len(weights)
        if caps is None:
            caps = np.full(n, self.cap, dtype=np.float64)
        else:
            caps = np.asarray(caps, dtype=np.float64)

        w = np.asarray(weights, dtype=np.float64)
        w = np.maximum(w, 0.0)
        total = w.sum()
        w = w / total if total > 1e-9 else np.ones(n) / n

        loads = np.asarray(loads, dtype=np.float64)
        fired = [False] * n
        converged = True
        iters = 0

        for it in range(self.max_iter):
            h = caps - loads                      # h_i = cap_i - load_i
            violations = h < 0.0
            if not violations.any():
                break

            grad = np.where(violations, self.lam * (-h), 0.0)  # push down violators
            w_new = self._project_simplex(w - self.lr * grad)
            delta = float(np.abs(w_new - w).max())
            w = w_new
            iters = it + 1

            for i in range(n):
                if violations[i]:
                    fired[i] = True

            if delta < self.tol:
                break
        else:
            converged = False

        return CBFResult(weights=w, fired=fired, iterations=iters, converged=converged)

    @staticmethod
    def _project_simplex(v: np.ndarray) -> np.ndarray:
        """
        Euclidean projection onto the probability simplex in O(n log n).

        Duchi, Shalev-Shwartz, Singer, Chandra (2008):
        'Efficient Projections onto the l1-Ball for Learning in High Dimensions'.
        """
        n = len(v)
        u = np.sort(v)[::-1]
        cssv = np.cumsum(u)
        rho_candidates = u - (cssv - 1.0) / np.arange(1, n + 1) > 0
        if not rho_candidates.any():
            return np.ones(n) / n
        rho = int(np.where(rho_candidates)[0][-1])
        theta = (cssv[rho] - 1.0) / (rho + 1)
        return np.maximum(v - theta, 0.0)


# ---------------------------------------------------------------------------
# SafetyMonitor — wraps CBFProjector with violation tracking
# ---------------------------------------------------------------------------

class SafetyMonitor:
    """
    Production wrapper around :class:`CBFProjector` that:

    - Tracks per-backend violation rate over a rolling window.
    - Keeps a bounded log of recent violation events.
    - Exposes an ``audit()`` dict for the dashboard and alerting.

    Thread-safe via an internal ``threading.Lock``.

    Example::

        monitor = SafetyMonitor(n_backends=4, cap=0.80)

        # In the control loop:
        safe_w, fired = monitor.step(kan_weights, backend_loads)

        # In the admin/metrics endpoint:
        print(monitor.audit())
    """

    def __init__(
        self,
        n_backends: int,
        cap: float = 0.80,
        lam: float = 0.5,
        lr: float = 0.01,
        max_iter: int = 100,
        history_len: int = 300,  # ticks per backend
    ) -> None:
        self.n = n_backends
        self.projector = CBFProjector(cap=cap, lam=lam, lr=lr, max_iter=max_iter)

        self._lock = threading.Lock()
        # Rolling violation flag history per backend
        self._vhist: list[deque[int]] = [
            deque(maxlen=history_len) for _ in range(n_backends)
        ]
        # Bounded event log
        self._vlog: deque[CBFViolation] = deque(maxlen=1000)

        # Cumulative counters
        self.total_projections: int = 0
        self.total_violations: int = 0
        self._total_iters: float = 0.0

    # ------------------------------------------------------------------ #
    #  Step                                                                #
    # ------------------------------------------------------------------ #

    def step(
        self,
        weights: np.ndarray,
        loads: np.ndarray,
        caps: np.ndarray | None = None,
    ) -> tuple[np.ndarray, list[bool]]:
        """
        Run one CBF projection step and record metrics.

        Args:
            weights: raw KAN routing weights.
            loads:   current backend utilisation.
            caps:    optional per-backend cap overrides.

        Returns:
            (safe_weights, cbf_fired)
        """
        result = self.projector.project_detailed(weights, loads, caps)

        with self._lock:
            self.total_projections += 1
            self._total_iters += result.iterations

            raw_total = float(np.asarray(weights).sum())
            for i in range(self.n):
                fired = result.fired[i]
                self._vhist[i].append(1 if fired else 0)
                if fired:
                    self.total_violations += 1
                    self._vlog.append(
                        CBFViolation(
                            timestamp=time.time(),
                            backend_id=i,
                            load=float(loads[i]),
                            cap=self.projector.cap,
                            weight_before=(
                                float(weights[i]) / raw_total
                                if raw_total > 1e-9
                                else 0.0
                            ),
                            weight_after=float(result.weights[i]),
                        )
                    )

        return result.weights, result.fired

    # ------------------------------------------------------------------ #
    #  Metrics                                                             #
    # ------------------------------------------------------------------ #

    def violation_rate(self, backend_id: int) -> float:
        """Rolling violation rate for backend *backend_id* in [0, 1]."""
        hist = self._vhist[backend_id]
        if not hist:
            return 0.0
        return sum(hist) / len(hist)

    def violation_rates(self) -> list[float]:
        """Per-backend rolling violation rates."""
        return [self.violation_rate(i) for i in range(self.n)]

    def recent_violations(self, n: int = 20) -> list[dict]:
        """Last *n* violation events, most recent first."""
        with self._lock:
            recent = list(self._vlog)[-n:]
        return [
            {
                "t": time.strftime("%H:%M:%S", time.localtime(v.timestamp)),
                "backend": v.backend_id,
                "load": round(v.load, 3),
                "cap": v.cap,
                "w_before": round(v.weight_before, 4),
                "w_after": round(v.weight_after, 4),
            }
            for v in reversed(recent)
        ]

    def audit(self) -> dict:
        """JSON-serialisable snapshot for monitoring / alerting endpoints."""
        with self._lock:
            mean_iters = (
                self._total_iters / self.total_projections
                if self.total_projections > 0
                else 0.0
            )
        return {
            "total_projections": self.total_projections,
            "total_violations": self.total_violations,
            "mean_iters_per_step": round(mean_iters, 2),
            "violation_rates": [round(r, 4) for r in self.violation_rates()],
            "cap": self.projector.cap,
            "recent_violations": self.recent_violations(5),
        }

    # ------------------------------------------------------------------ #
    #  Configuration                                                       #
    # ------------------------------------------------------------------ #

    def set_cap(self, cap: float) -> None:
        """Update the default cap at runtime (affects future projections)."""
        with self._lock:
            self.projector.cap = cap

    def set_backend_caps(self, caps: list[float]) -> None:
        """
        Pre-set per-backend caps.  They are passed to each ``step()`` call.
        Call this once at startup; the proxy uses the returned caps array.
        """
        self._backend_caps = np.array(caps, dtype=np.float64)

    def get_backend_caps(self) -> np.ndarray | None:
        """Return per-backend caps array if configured, else None."""
        return getattr(self, "_backend_caps", None)
