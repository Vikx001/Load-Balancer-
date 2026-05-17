"""
Load Balancer Simulation Environment — Omega-LB
================================================
M/M/1 queue simulation for offline RL training and integration tests.

This is a standalone, importable version of the environment that also lives
inside ``ml/ppo/train_ppo_kan.py``.  Keeping it here avoids a circular
import and makes it easy to spin up the env from CI scripts or notebooks
without importing the full training stack.

Usage::

    from ml.simulation import LBSimEnv

    env = LBSimEnv(n_backends=4)
    state = env.reset()

    weights = np.array([0.25, 0.25, 0.25, 0.25])
    next_state, reward, done, info = env.step(weights)
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from typing import Any

import numpy as np


# ---------------------------------------------------------------------------
# Environment configuration
# ---------------------------------------------------------------------------

@dataclass
class SimConfig:
    """
    All tunable knobs for the simulation.

    Mirrors the fields of ``PPOConfig`` that the environment actually uses,
    so you can pass a ``PPOConfig`` directly or create a lightweight
    ``SimConfig`` for integration tests.
    """
    num_backends: int = 4
    # Reward weights
    alpha: float = 0.01    # P99 latency penalty
    beta: float = 0.1      # load variance penalty
    gamma_r: float = 0.001 # throughput bonus
    delta: float = 1.0     # capacity violation penalty
    # Episode length
    episode_len: int = 2048
    # Traffic pattern
    base_rps: float = 500.0
    amp_rps: float = 300.0
    rps_period: float = 100.0   # steps per sine cycle
    # Backend capacity (reqs/step for M/M/1 model)
    capacity: float = 100.0
    service_rate: float = 10.0
    # Random seed
    seed: int = 42

    @property
    def state_dim(self) -> int:
        """Feature vector length: 8 per backend + 4 global."""
        return self.num_backends * 8 + 4


# ---------------------------------------------------------------------------
# LBSimEnv
# ---------------------------------------------------------------------------

class LBSimEnv:
    """
    Simulated load balancer environment.

    State vector (per call to ``_state()``):

    ====  ==========================================
    Idx   Feature
    ====  ==========================================
    8i+0  cpu_utilisation          (load / cap)
    8i+1  active_connections       (load)
    8i+2  queue_depth              (max 0, load-50)
    8i+3  ewma_latency_ms          (cpu × 100)
    8i+4  tx_bytes_per_sec         (load × 1024)
    8i+5  rx_bytes_per_sec         (load × 512)
    8i+6  health_status            (always 1.0 here)
    8i+7  error_rate_1m            (ramp from 0.8·cap)
    N×8   total_rps                (sum of loads)
    N×8+1 p99_latency_ms           (max cpu × 200)
    N×8+2 time_sin                 (sin(2π·t))
    N×8+3 time_cos                 (cos(2π·t))
    ====  ==========================================

    Reward::

        r = −α·P99 − β·Var(cpu) × 100 + γ·RPS/10 − δ·violations
    """

    def __init__(
        self,
        n_backends: int = 4,
        cfg: SimConfig | None = None,
    ) -> None:
        if cfg is None:
            cfg = SimConfig(num_backends=n_backends)
        self.cfg = cfg
        self.n = cfg.num_backends
        self.rng = np.random.default_rng(cfg.seed)

        # State
        self.loads: np.ndarray = np.zeros(self.n)
        self.capacities: np.ndarray = np.ones(self.n) * cfg.capacity
        self.step_count: int = 0

        # Expose for RL wrappers
        self.state_dim: int = cfg.state_dim
        self.action_dim: int = self.n  # softmax weights

    # ------------------------------------------------------------------ #
    #  Gym-compatible interface                                            #
    # ------------------------------------------------------------------ #

    def reset(self, seed: int | None = None) -> tuple[np.ndarray, dict]:
        """
        Reset to a random initial state.

        Returns:
            (observation, info)
        """
        if seed is not None:
            self.rng = np.random.default_rng(seed)
        self.loads = self.rng.uniform(0, 0.3, self.n) * self.capacities
        self.step_count = 0
        return self._state(), {}

    def step(
        self, weights: np.ndarray
    ) -> tuple[np.ndarray, float, bool, bool, dict[str, Any]]:
        """
        Apply routing *weights* for one time step.

        Args:
            weights: routing weight vector, shape (N,).
                     Need not sum to 1 — will be normalised.

        Returns:
            (next_state, reward, terminated, truncated, info)

        The ``done`` flag used by the PPO trainer is ``terminated or truncated``.
        For compatibility with the older 3-tuple API, use
        ``env.step_compat(weights)`` which returns ``(state, reward, done)``.
        """
        weights = np.asarray(weights, dtype=np.float64)
        total_w = weights.sum()
        if total_w < 1e-9:
            weights = np.ones(self.n) / self.n
        else:
            weights = weights / total_w

        # Sinusoidal traffic pattern
        total_rps = (
            self.cfg.base_rps
            + self.cfg.amp_rps * math.sin(self.step_count / self.cfg.rps_period)
        )
        arrivals = weights * total_rps

        # M/M/1 queue update
        self.loads = np.maximum(
            0.0, self.loads + arrivals - self.cfg.service_rate
        )
        self.loads = np.minimum(self.loads, self.capacities * 1.2)

        # Metrics
        cpu = self.loads / self.capacities
        p99_ms = float(np.max(cpu) * 200 + self.rng.exponential(5))
        variance = float(np.var(cpu))
        violations = int(np.any(cpu > 1.0))

        reward = (
            -self.cfg.alpha  * p99_ms
            - self.cfg.beta  * variance * 100
            + self.cfg.gamma_r * total_rps / 10
            - self.cfg.delta * violations
        )

        self.step_count += 1
        terminated = False
        truncated = self.step_count >= self.cfg.episode_len

        info = {
            "total_rps": total_rps,
            "p99_ms": p99_ms,
            "cpu": cpu.tolist(),
            "violations": violations,
        }
        return self._state(), reward, terminated, truncated, info

    def step_compat(
        self, weights: np.ndarray
    ) -> tuple[np.ndarray, float, bool]:
        """
        3-tuple API compatible with the PPOTrainer rollout loop.

        Returns:
            (next_state, reward, done)
        """
        state, reward, terminated, truncated, _ = self.step(weights)
        return state, reward, terminated or truncated

    # ------------------------------------------------------------------ #
    #  Private                                                             #
    # ------------------------------------------------------------------ #

    def _state(self) -> np.ndarray:
        per_server: list[float] = []
        cpu = self.loads / self.capacities
        for i in range(self.n):
            per_server.extend([
                float(cpu[i]),                           # cpu_utilisation
                float(self.loads[i]),                    # active_connections
                max(0.0, float(self.loads[i] - 50)),     # queue_depth
                float(cpu[i] * 100),                     # ewma_latency_ms
                float(self.loads[i] * 1024),             # tx_bytes_per_sec
                float(self.loads[i] * 512),              # rx_bytes_per_sec
                1.0,                                     # health_status
                max(0.0, (float(cpu[i]) - 0.8) * 0.5),  # error_rate_1m
            ])
        t = self.step_count / 2000.0
        global_feats = [
            float(np.sum(self.loads)),                          # total_rps
            float(np.max(cpu) * 200),                          # p99_latency_ms
            math.sin(2 * math.pi * t),                         # time_sin
            math.cos(2 * math.pi * t),                         # time_cos
        ]
        return np.array(per_server + global_feats, dtype=np.float32)
