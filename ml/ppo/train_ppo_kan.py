"""
Layer 2 + 3: PPO with KAN actor training.
Trains the KAN actor + MLP critic using PPO (Proximal Policy Optimization).
After training, extracts symbolic equations from the KAN actor for the audit log.
Exports the trained KAN actor to ONNX for Go inference.

Reference:
  - arXiv 2401.05525 (Huawei Safe LB with DRL+CBF)
  - arXiv 2505.14459 (KAN interpretable RL for LB)
"""

from __future__ import annotations

import math
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F
from torch.optim import Adam

# ─── Hyperparameters ──────────────────────────────────────────────────────────

@dataclass
class PPOConfig:
    # Environment
    num_backends: int = 4
    state_dim: int = 0  # computed: num_backends*8 + 4

    # PPO
    lr_actor: float = 3e-4
    lr_critic: float = 3e-4
    gamma: float = 0.99
    lam: float = 0.95          # GAE lambda
    clip_eps: float = 0.2
    vf_coef: float = 0.5
    ent_coef: float = 0.01
    max_grad_norm: float = 0.5
    update_epochs: int = 10
    batch_size: int = 512
    rollout_steps: int = 2048

    # KAN
    kan_spline_order: int = 3   # cubic B-spline
    kan_grid_size: int = 5      # grid points per spline

    # Training
    total_steps: int = 1_000_000
    action_smoothing: float = 0.7

    # CBF
    cbf_lambda: float = 0.5
    capacity_cap: float = 0.80

    # Reward weights (α, β, γ, δ)
    alpha: float = 0.5   # latency
    beta: float = 0.3    # variance
    gamma_r: float = 0.2 # throughput
    delta: float = 1000  # violation penalty

    def __post_init__(self):
        self.state_dim = self.num_backends * 8 + 4


# ─── KAN Layer: B-spline activations on edges ─────────────────────────────────

class KANLayer(nn.Module):
    """
    One layer of a Kolmogorov-Arnold Network.
    Each input feature i → output feature j through a learned B-spline φ_{ij}.
    Nodes sum their inputs: output_j = Σ_i φ_{ij}(x_i).
    """

    def __init__(self, in_features: int, out_features: int,
                 grid_size: int = 5, spline_order: int = 3):
        super().__init__()
        self.in_features = in_features
        self.out_features = out_features
        self.grid_size = grid_size
        self.spline_order = spline_order

        # B-spline control points: one per (in, out) edge
        # Shape: (out_features, in_features, grid_size + spline_order)
        # Number of basis functions = grid_size + spline_order
        n_coeff = grid_size + spline_order
        self.coeff = nn.Parameter(
            torch.randn(out_features, in_features, n_coeff) * 0.1
        )
        # Residual linear (SiLU-activated) for training stability
        self.residual = nn.Linear(in_features, out_features, bias=True)
        # Scale of residual vs spline contribution
        self.scale = nn.Parameter(torch.ones(out_features, in_features))

        # Extended knot vector with repeated boundary knots for clamped B-splines
        # Interior: grid_size+1 points from -1 to 1
        # Pad spline_order knots on each side → total = grid_size + 1 + 2*spline_order
        interior = torch.linspace(-1, 1, grid_size + 1)
        grid = torch.cat([
            interior[0].expand(spline_order),
            interior,
            interior[-1].expand(spline_order),
        ])
        self.register_buffer("grid", grid)
        self._n_coeff = n_coeff

    def b_splines(self, x: torch.Tensor) -> torch.Tensor:
        """
        Evaluate B-spline basis functions of degree `spline_order` at x.
        Uses extended clamped knot vector.
        x: (batch, in_features)
        Returns: (batch, in_features, grid_size + spline_order)
        """
        x = x.unsqueeze(-1)  # (batch, in, 1)
        grid = self.grid     # length = grid_size + 1 + 2*spline_order

        # Order-0 basis over all knot intervals
        basis = ((x >= grid[:-1]) & (x < grid[1:])).float()
        # Handle right boundary exactly: x = grid[-1] must land in the last
        # non-degenerate interval (index n_coeff-1), not the degenerate trailing ones.
        last_proper = self._n_coeff - 1
        at_right = (x == grid[-1]).float()  # (batch, in, 1)
        basis[..., last_proper:last_proper+1] = (
            basis[..., last_proper:last_proper+1] + at_right
        )

        # Cox-de Boor recursion — produces n_coeff = grid_size + spline_order functions
        for k in range(1, self.spline_order + 1):
            n = basis.shape[-1] - 1  # number of output functions at this level
            lg = grid[:n + 1]   # left knot endpoints
            rg = grid[k:k + n + 1]  # right knot endpoints
            dl = rg[:-1] - lg[:-1]
            dr = rg[1:]  - lg[1:]
            # Standard B-spline convention: 0/0 = 0 (degenerate knot spans → zero term)
            left  = torch.where(dl.abs() > 1e-10,
                                (x - lg[:-1]) / dl.clamp(min=1e-10) * basis[..., :n],
                                torch.zeros_like(basis[..., :n]))
            right = torch.where(dr.abs() > 1e-10,
                                (rg[1:] - x)  / dr.clamp(min=1e-10) * basis[..., 1:n + 1],
                                torch.zeros_like(basis[..., 1:n + 1]))
            basis = left + right

        return basis  # (batch, in, grid_size + spline_order)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        # x: (batch, in_features) — normalised to [-1, 1]
        x_clamped = x.clamp(-1, 1)
        basis = self.b_splines(x_clamped)  # (batch, in, n_coeff)

        # Spline output: Σ_k coeff[out, in, k] * basis[batch, in, k]
        # → (batch, out, in) then sum over in
        spline_out = torch.einsum("bin,oin->bo", basis, self.coeff)

        # Residual (SiLU)
        res_out = self.residual(F.silu(x))

        return spline_out + res_out


# ─── KAN Actor (Layer 3): interpretable routing policy ─────────────────────

class KANActor(nn.Module):
    """
    1-layer KAN actor. Takes compressed state features (PCA top-k) and
    outputs routing weights (softmax over N backends).

    After training: call extract_equations() to get the symbolic routing policy.
    """

    def __init__(self, cfg: PPOConfig):
        super().__init__()
        # PCA compression: reduce full state to top-8 features
        self.pca_dim = min(8, cfg.state_dim)
        self.pca = nn.Linear(cfg.state_dim, self.pca_dim, bias=False)

        # Single KAN layer
        self.kan = KANLayer(
            in_features=self.pca_dim,
            out_features=cfg.num_backends,
            grid_size=cfg.kan_grid_size,
            spline_order=cfg.kan_spline_order,
        )

    def forward(self, state: torch.Tensor) -> torch.Tensor:
        """
        state: (batch, state_dim) — raw MDP state
        Returns: (batch, num_backends) — routing weights (sum to 1)
        """
        compressed = self.pca(state)
        # Normalise to [-1, 1] for B-spline domain
        compressed = torch.tanh(compressed)
        logits = self.kan(compressed)
        return F.softmax(logits, dim=-1)

    def extract_equations(self) -> list[str]:
        """
        Extract human-readable symbolic equations from the learned B-splines.
        Uses polynomial approximation of each spline edge.
        Returns one equation string per backend output.
        """
        equations = []
        with torch.no_grad():
            # Sample spline functions on a grid and fit degree-2 polynomial
            x_grid = torch.linspace(-1, 1, 50)
            for out_idx in range(self.kan.out_features):
                terms = []
                for in_idx in range(self.kan.in_features):
                    x_1d = x_grid.unsqueeze(0)  # (1, 50)
                    # Evaluate this single (in, out) edge
                    x_full = torch.zeros(50, self.kan.in_features)
                    x_full[:, in_idx] = x_grid
                    basis = self.kan.b_splines(x_full.clamp(-1, 1))
                    coeff = self.kan.coeff[out_idx, in_idx, :]
                    y = (basis[:, in_idx, :] * coeff).sum(-1).numpy()

                    # Fit degree-2 polynomial
                    x_np = x_grid.numpy()
                    p = np.polyfit(x_np, y, deg=2)
                    a, b, c = p
                    if abs(a) < 1e-3 and abs(b) < 1e-3:
                        continue  # near-zero edge
                    term = f"{a:+.3f}·f{in_idx}² {b:+.3f}·f{in_idx} {c:+.3f}"
                    terms.append(term)

                eq = f"w_{out_idx} = softmax({' '.join(terms) or '0'})"
                equations.append(eq)
        return equations


# ─── MLP Critic ───────────────────────────────────────────────────────────────

class MLPCritic(nn.Module):
    def __init__(self, state_dim: int):
        super().__init__()
        self.net = nn.Sequential(
            nn.Linear(state_dim, 256), nn.LayerNorm(256), nn.GELU(),
            nn.Linear(256, 128),        nn.LayerNorm(128), nn.GELU(),
            nn.Linear(128, 1),
        )

    def forward(self, state: torch.Tensor) -> torch.Tensor:
        return self.net(state).squeeze(-1)


# ─── CBF Projection ───────────────────────────────────────────────────────────

def cbf_project(weights: torch.Tensor, loads: torch.Tensor,
                lam: float = 0.5, cap: float = 0.80,
                lr: float = 0.05, max_iter: int = 200) -> torch.Tensor:
    """
    Project weights onto safe region via projected gradient descent.
    h_i(x) = cap - load_i ≥ 0 for all i.
    weights: (batch, N), loads: (batch, N) — current load fractions.
    """
    w = weights.clone().detach().requires_grad_(True)
    raw = weights.clone().detach()

    for _ in range(max_iter):
        # h_i = cap - (load_i + w_i) → safe if > 0
        h = cap - (loads + w)
        violated = (h < 0)
        if not violated.any():
            break

        # Gradient: proximity + CBF correction
        grad = 2 * (w - raw) - lam * h.clamp(max=0)
        w = (w - lr * grad).detach().requires_grad_(True)
        # Project onto simplex
        w = simplex_project(w)

    return w.detach()


def simplex_project(v: torch.Tensor) -> torch.Tensor:
    """Project rows of v onto the probability simplex."""
    n = v.shape[-1]
    u, _ = torch.sort(v, dim=-1, descending=True)
    cssv = u.cumsum(dim=-1)
    rho = (u - (cssv - 1) / torch.arange(1, n + 1, device=v.device).float() > 0)
    rho_idx = rho.long().sum(dim=-1, keepdim=True) - 1
    theta = (cssv.gather(-1, rho_idx) - 1) / (rho_idx.float() + 1)
    return (v - theta).clamp(min=0)


# ─── Rollout Buffer ───────────────────────────────────────────────────────────

@dataclass
class RolloutBuffer:
    states:   list = field(default_factory=list)
    actions:  list = field(default_factory=list)
    rewards:  list = field(default_factory=list)
    values:   list = field(default_factory=list)
    log_probs: list = field(default_factory=list)
    dones:    list = field(default_factory=list)

    def clear(self):
        self.states.clear(); self.actions.clear(); self.rewards.clear()
        self.values.clear(); self.log_probs.clear(); self.dones.clear()

    def compute_gae(self, gamma: float, lam: float, last_value: float) -> tuple:
        """Compute GAE advantages and returns."""
        values = self.values + [last_value]
        advantages = []
        gae = 0.0
        for t in reversed(range(len(self.rewards))):
            delta = self.rewards[t] + gamma * values[t+1] * (1 - self.dones[t]) - values[t]
            gae = delta + gamma * lam * (1 - self.dones[t]) * gae
            advantages.insert(0, gae)
        returns = [a + v for a, v in zip(advantages, self.values)]
        return advantages, returns


# ─── PPO Trainer ──────────────────────────────────────────────────────────────

class PPOTrainer:
    def __init__(self, cfg: PPOConfig):
        self.cfg = cfg
        self.actor  = KANActor(cfg)
        self.critic = MLPCritic(cfg.state_dim)
        self.opt_actor  = Adam(self.actor.parameters(),  lr=cfg.lr_actor)
        self.opt_critic = Adam(self.critic.parameters(), lr=cfg.lr_critic)
        self.buffer = RolloutBuffer()
        self.prev_weights = None

    def select_action(self, state: np.ndarray) -> tuple[np.ndarray, float, float]:
        """Sample action from KAN actor, apply action smoothing."""
        state_t = torch.FloatTensor(state).unsqueeze(0)
        with torch.no_grad():
            weights = self.actor(state_t).squeeze(0)
            value   = self.critic(state_t).item()

        # Action smoothing (prevents thundering herd)
        if self.prev_weights is not None:
            alpha = self.cfg.action_smoothing
            weights = alpha * weights + (1 - alpha) * self.prev_weights
            # Re-normalise after smoothing
            weights = weights / weights.sum()
        self.prev_weights = weights.clone()

        # Dirichlet log_prob as a differentiable surrogate for the softmax policy
        dist = torch.distributions.Dirichlet(weights * 10 + 1e-6)
        action = dist.sample()
        log_prob = dist.log_prob(action).item()
        return action.numpy(), log_prob, value

    def compute_reward(self, p99_ms: float, load_variance: float,
                       throughput: float, violations: int) -> float:
        cfg = self.cfg
        r = (-cfg.alpha * p99_ms
             - cfg.beta  * load_variance
             + cfg.gamma_r * throughput
             - cfg.delta  * violations)
        return r

    def update(self):
        """One PPO update cycle (10 epochs over rollout buffer)."""
        adv, ret = self.buffer.compute_gae(
            self.cfg.gamma, self.cfg.lam, last_value=0.0
        )
        states    = torch.FloatTensor(np.array(self.buffer.states))
        actions   = torch.FloatTensor(np.array(self.buffer.actions))
        old_lp    = torch.FloatTensor(self.buffer.log_probs)
        advantages = torch.FloatTensor(adv)
        returns    = torch.FloatTensor(ret)
        advantages = (advantages - advantages.mean()) / (advantages.std() + 1e-8)

        n = len(states)
        idx = np.arange(n)

        for _ in range(self.cfg.update_epochs):
            np.random.shuffle(idx)
            for start in range(0, n, self.cfg.batch_size):
                batch = idx[start:start + self.cfg.batch_size]
                bs, ba = states[batch], actions[batch]
                bold_lp, badv, bret = old_lp[batch], advantages[batch], returns[batch]

                # Actor forward
                new_weights = self.actor(bs)
                dist = torch.distributions.Dirichlet(new_weights * 10 + 1e-6)
                new_lp = dist.log_prob(ba)
                entropy = dist.entropy().mean()

                ratio = (new_lp - bold_lp).exp()
                surr1 = ratio * badv
                surr2 = ratio.clamp(1 - self.cfg.clip_eps, 1 + self.cfg.clip_eps) * badv
                actor_loss = -torch.min(surr1, surr2).mean() - self.cfg.ent_coef * entropy

                self.opt_actor.zero_grad()
                actor_loss.backward()
                nn.utils.clip_grad_norm_(self.actor.parameters(), self.cfg.max_grad_norm)
                self.opt_actor.step()

                # Critic forward
                values = self.critic(bs)
                critic_loss = self.cfg.vf_coef * F.mse_loss(values, bret)
                self.opt_critic.zero_grad()
                critic_loss.backward()
                nn.utils.clip_grad_norm_(self.critic.parameters(), self.cfg.max_grad_norm)
                self.opt_critic.step()

        self.buffer.clear()

    def export_onnx(self, path: str):
        """Export the KAN actor to ONNX for Go inference via onnxruntime."""
        dummy = torch.zeros(1, self.cfg.state_dim)
        torch.onnx.export(
            self.actor,
            dummy,
            path,
            input_names=["state"],
            output_names=["weights"],
            dynamic_axes={"state": {0: "batch"}, "weights": {0: "batch"}},
            opset_version=17,
        )
        print(f"KAN actor exported to ONNX: {path}")

    def write_audit_log(self, version: str, log_path: str):
        """Write extracted symbolic equations to audit log (SRE-readable)."""
        equations = self.actor.extract_equations()
        with open(log_path, "a") as f:
            f.write(f"\n=== KAN Policy Update: {version} ({time.strftime('%Y-%m-%dT%H:%M:%SZ')}) ===\n")
            for eq in equations:
                f.write(f"  {eq}\n")
        print(f"KAN audit log updated: {log_path}")
        return equations


# ─── Load Balancer Simulation Environment ─────────────────────────────────────

class LBSimEnv:
    """
    Simulated load balancer environment for offline PPO training.
    In production, replace with NS3-based simulation or real traffic replay.
    """

    def __init__(self, cfg: PPOConfig):
        self.cfg = cfg
        self.n = cfg.num_backends
        self.rng = np.random.default_rng(42)
        self.loads = np.zeros(self.n)
        self.capacities = np.ones(self.n) * 100
        self.step_count = 0

    def reset(self) -> np.ndarray:
        self.loads = self.rng.uniform(0, 0.3, self.n) * self.capacities
        self.step_count = 0
        return self._state()

    def step(self, weights: np.ndarray) -> tuple[np.ndarray, float, bool]:
        # Simulate arriving requests
        total_rps = 500 + 300 * math.sin(self.step_count / 100)
        arrivals = weights * total_rps

        # Update loads (simplified M/M/1 queue model)
        service_rate = 10.0
        self.loads = np.maximum(0, self.loads + arrivals - service_rate)
        self.loads = np.minimum(self.loads, self.capacities * 1.2)  # allow slight overflow

        # Compute metrics
        load_fracs = self.loads / self.capacities
        p99_ms = float(np.max(load_fracs) * 200 + self.rng.exponential(5))
        variance = float(np.var(load_fracs))
        violations = int(np.any(load_fracs > 1.0))

        reward = (-self.cfg.alpha * p99_ms
                  - self.cfg.beta * variance * 100
                  + self.cfg.gamma_r * total_rps / 10
                  - self.cfg.delta * violations)

        self.step_count += 1
        done = self.step_count >= 2048

        # Sinusoidal time encoding
        t = self.step_count / 2000
        time_features = [math.sin(2 * math.pi * t), math.cos(2 * math.pi * t)]

        return self._state(), reward, done

    def _state(self) -> np.ndarray:
        per_server = []
        for i in range(self.n):
            cpu = self.loads[i] / self.capacities[i]
            per_server.extend([
                cpu,                              # cpu_utilisation
                float(self.loads[i]),             # active_connections
                max(0, float(self.loads[i] - 50)), # queue_depth
                cpu * 100,                        # ewma_latency_ms
                float(self.loads[i] * 1024),      # tx_bytes_per_sec
                float(self.loads[i] * 512),       # rx_bytes_per_sec
                1.0,                              # health_status
                max(0, (cpu - 0.8) * 0.5),        # error_rate_1m
            ])
        t = self.step_count / 2000
        global_feats = [
            sum(self.loads),                      # total_rps
            max(self.loads / self.capacities) * 200, # p99_latency_ms
            math.sin(2 * math.pi * t),            # time_sin
            math.cos(2 * math.pi * t),            # time_cos
        ]
        return np.array(per_server + global_feats, dtype=np.float32)


# ─── Training Entry Point ─────────────────────────────────────────────────────

def train(cfg: Optional[PPOConfig] = None, output_dir: str = "models"):
    if cfg is None:
        cfg = PPOConfig(num_backends=4)

    Path(output_dir).mkdir(parents=True, exist_ok=True)
    env = LBSimEnv(cfg)
    trainer = PPOTrainer(cfg)

    state = env.reset()
    total_steps = 0
    episode = 0

    print(f"Training Omega-LB PPO+KAN actor | state_dim={cfg.state_dim} | backends={cfg.num_backends}")

    while total_steps < cfg.total_steps:
        # Collect rollout
        for _ in range(cfg.rollout_steps):
            action, log_prob, value = trainer.select_action(state)

            # CBF projection (training-time safety enforcement)
            loads_t = torch.FloatTensor(state[:cfg.num_backends * 8:8]).unsqueeze(0)
            action_t = torch.FloatTensor(action).unsqueeze(0)
            safe_action = cbf_project(action_t, loads_t,
                                      lam=cfg.cbf_lambda, cap=cfg.capacity_cap)
            safe_action_np = safe_action.squeeze(0).numpy()

            next_state, reward, done = env.step(safe_action_np)

            trainer.buffer.states.append(state)
            trainer.buffer.actions.append(safe_action_np)
            trainer.buffer.rewards.append(reward)
            trainer.buffer.values.append(value)
            trainer.buffer.log_probs.append(log_prob)
            trainer.buffer.dones.append(float(done))

            state = next_state
            total_steps += 1
            if done:
                state = env.reset()
                episode += 1

        # PPO update
        trainer.update()

        if total_steps % 10000 == 0:
            print(f"  steps={total_steps} | episode={episode}")

    # Export
    onnx_path = f"{output_dir}/kan_actor.onnx"
    trainer.export_onnx(onnx_path)
    trainer.write_audit_log("v1.0", f"{output_dir}/kan_audit.log")

    # Save PyTorch checkpoint
    torch.save({
        "actor": trainer.actor.state_dict(),
        "critic": trainer.critic.state_dict(),
        "config": cfg,
    }, f"{output_dir}/ppo_kan_checkpoint.pt")

    return trainer


if __name__ == "__main__":
    train()
