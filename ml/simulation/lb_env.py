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
    delta: float = 1.0     # capacity violation penalty (soft component)
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

    # Hard-floor penalty thresholds.
    # These are discrete cliff penalties (not soft-scaled) and represent
    # absolute constraints.  Soft penalty alone (delta * violations) lets the
    # agent learn to tolerate violations when throughput is high enough.
    # Hard floors prevent this Goodhart's Law collapse.
    hard_floor_cpu: float = 0.95    # any backend above this triggers -1000 penalty
    hard_floor_error: float = 0.01  # aggregate error rate above this triggers -500
    hard_floor_p99_ms: float = 500.0  # P99 above this triggers -200 penalty

    # Deceptive server scenario.
    # Indices of backends that behave like "deceptive servers": they accept
    # requests and report low latency until they hit 60% CPU, after which
    # their latency spikes 10× with no advance warning in the state vector.
    # This tests whether the policy learns to hedge against unseen overload.
    # Leave empty to disable (default: off in unit tests, on in adversarial
    # scenario tests).
    deceptive_servers: list[int] = field(default_factory=list)

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
    8i+3  ewma_latency_ms          (cpu × 100, or 10× for deceptive servers)
    8i+4  tx_bytes_per_sec         (load × 1024)
    8i+5  rx_bytes_per_sec         (load × 512)
    8i+6  health_status            (always 1.0 here)
    8i+7  error_rate_1m            (ramp from 0.8·cap)
    N×8   total_rps                (sum of loads)
    N×8+1 p99_latency_ms           (max effective latency)
    N×8+2 time_sin                 (sin(2π·t))
    N×8+3 time_cos                 (cos(2π·t))
    ====  ==========================================

    Reward (hard floor + soft penalty)::

        r = −α·P99 − β·Var(cpu)×100 + γ·RPS/10 − δ·violations
              − 1000 [if any cpu > hard_floor_cpu]
              − 500  [if error_rate > hard_floor_error]
              − 200  [if P99 > hard_floor_p99_ms]
    """

    # ------------------------------------------------------------------
    # Class-level reward-hacking detector.
    # Tracks the running ratio of 429-rejected requests to admitted ones.
    # If the agent is gaming the reward by shedding load before it appears
    # in the latency histogram (i.e. rejected requests don't show up as
    # high P99), the rejection rate will climb without a latency increase.
    # When that ratio exceeds the threshold, a warning is logged and an
    # extra penalty is applied.
    REJECTION_RATE_WARN: float = 0.30  # 30% rejection is reward hacking
    REJECTION_RATE_PENALTY: float = 300.0

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

        # Reward-hacking detection: track admitted vs rejected traffic.
        self._admitted_rps: float = 0.0
        self._rejected_rps: float = 0.0

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
        self._admitted_rps = 0.0
        self._rejected_rps = 0.0
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

        # Deceptive server: report normal latency until 60% CPU, then spike 10×.
        # This models servers that silently queue connections before failing
        # (keep-alive pools, connection limits, kernel TCP backlog exhaustion).
        # Agents that do not hedge against this pattern will over-route to these
        # backends and then experience a sudden latency cliff.
        cpu = self.loads / self.capacities
        effective_latency_ms = cpu * 100.0  # base latency model
        for i in self.cfg.deceptive_servers:
            if 0 <= i < self.n and cpu[i] > 0.60:
                effective_latency_ms[i] = cpu[i] * 1000.0  # 10× spike

        p99_ms = float(np.max(effective_latency_ms) + self.rng.exponential(5))
        variance = float(np.var(cpu))
        violations = int(np.any(cpu > 1.0))
        error_rate = float(np.mean(np.maximum(0.0, (cpu - 0.8) * 0.5)))

        # Track admitted vs rejected for reward-hacking detection.
        admitted = float(np.sum(arrivals))
        # Rejected = requests that arrive but find the queue above 95% capacity.
        rejected = float(np.sum(
            np.maximum(0.0, arrivals - np.maximum(0.0, self.capacities - self.loads))
        ))
        self._admitted_rps = 0.9 * self._admitted_rps + 0.1 * admitted
        self._rejected_rps = 0.9 * self._rejected_rps + 0.1 * rejected

        # ─── REWARD COMPUTATION ───────────────────────────────────────────────
        # Soft component (shaped gradient):
        reward = (
            -self.cfg.alpha  * p99_ms
            - self.cfg.beta  * variance * 100
            + self.cfg.gamma_r * total_rps / 10
            - self.cfg.delta * violations
        )

        # Hard floors (cliff penalties): these are ABSOLUTE constraints that the
        # agent must not learn to trade off against throughput.  Without hard floors
        # a Goodhart's Law collapse occurs: the agent finds it profitable to sustain
        # overload for the throughput bonus, since delta*violations is too small.
        if float(np.max(cpu)) > self.cfg.hard_floor_cpu:
            reward -= 1000.0
        if error_rate > self.cfg.hard_floor_error:
            reward -= 500.0
        if p99_ms > self.cfg.hard_floor_p99_ms:
            reward -= 200.0

        # Reward-hacking detection: if the agent is gaming the reward by rejecting
        # traffic before it appears in the latency histogram (e.g. via 429 shedding
        # or by sending traffic to /dev/null backends), the rejection rate rises
        # without a proportional P99 increase.  Penalise this.
        rejection_rate = (
            self._rejected_rps / max(self._admitted_rps + self._rejected_rps, 1.0)
        )
        if rejection_rate > self.REJECTION_RATE_WARN:
            reward -= self.REJECTION_RATE_PENALTY * (
                rejection_rate - self.REJECTION_RATE_WARN
            )

        self.step_count += 1
        terminated = False
        truncated = self.step_count >= self.cfg.episode_len

        info = {
            "total_rps": total_rps,
            "p99_ms": p99_ms,
            "cpu": cpu.tolist(),
            "violations": violations,
            "error_rate": error_rate,
            "rejection_rate": rejection_rate,
            "reward_hacking_penalty": (
                self.REJECTION_RATE_PENALTY * max(0.0, rejection_rate - self.REJECTION_RATE_WARN)
            ),
            "deceptive_servers_active": [
                i for i in self.cfg.deceptive_servers
                if 0 <= i < self.n and cpu[i] > 0.60
            ],
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
            # For deceptive servers, the state vector deliberately shows the
            # pre-spike latency (cpu*100) so the agent must learn to hedge
            # without being given a warning signal.
            per_server.extend([
                float(cpu[i]),                           # cpu_utilisation
                float(self.loads[i]),                    # active_connections
                max(0.0, float(self.loads[i] - 50)),     # queue_depth
                float(cpu[i] * 100),                     # ewma_latency_ms (pre-spike)
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
