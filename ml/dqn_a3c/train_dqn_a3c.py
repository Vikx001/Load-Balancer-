"""
Layer 4: DQN + A3C adaptive rate limiting training.
Reference: arXiv 2511.03279 — Multi-Objective Adaptive Rate Limiting.

Architecture:
  - DQN (per service): discrete action (decrease/hold/increase 10%) + experience replay
  - A3C (global): asynchronous workers per service group share a global actor
"""

from __future__ import annotations

import copy
import random
import threading
from collections import deque
from dataclasses import dataclass
from typing import Optional

import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F
from torch.optim import Adam

# ─── Config ───────────────────────────────────────────────────────────────────


@dataclass
class DQNConfig:
    state_dim: int = 6  # [rps, cpu, queue, errRate, p99, limit]
    action_dim: int = 3  # decrease / hold / increase
    hidden: int = 128
    lr: float = 1e-3
    gamma: float = 0.99
    epsilon_start: float = 1.0
    epsilon_end: float = 0.05
    epsilon_decay: int = 10_000
    replay_capacity: int = 50_000
    batch_size: int = 64
    target_update_freq: int = 500
    total_steps: int = 100_000
    update_interval_ms: int = 100
    num_services: int = 4


# ─── Q-Network ────────────────────────────────────────────────────────────────


class QNetwork(nn.Module):
    def __init__(self, state_dim: int, action_dim: int, hidden: int = 128):
        super().__init__()
        self.net = nn.Sequential(
            nn.Linear(state_dim, hidden),
            nn.ReLU(),
            nn.Linear(hidden, hidden),
            nn.ReLU(),
            nn.Linear(hidden, action_dim),
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        return self.net(x)


# ─── Replay Buffer ────────────────────────────────────────────────────────────


class ReplayBuffer:
    def __init__(self, capacity: int):
        self.buf = deque(maxlen=capacity)

    def push(self, *transition):
        self.buf.append(transition)

    def sample(self, n: int):
        batch = random.sample(self.buf, n)
        return map(np.array, zip(*batch))

    def __len__(self):
        return len(self.buf)


# ─── DQN Agent (per service) ──────────────────────────────────────────────────


class DQNServiceAgent:
    def __init__(self, service_id: int, cfg: DQNConfig):
        self.service_id = service_id
        self.cfg = cfg
        self.policy_net = QNetwork(cfg.state_dim, cfg.action_dim, cfg.hidden)
        self.target_net = copy.deepcopy(self.policy_net)
        self.target_net.eval()
        self.opt = Adam(self.policy_net.parameters(), lr=cfg.lr)
        self.buffer = ReplayBuffer(cfg.replay_capacity)
        self.step_count = 0
        self.epsilon = cfg.epsilon_start

    def select_action(self, state: np.ndarray) -> int:
        # ε-greedy
        self.epsilon = max(self.cfg.epsilon_end, self.cfg.epsilon_start - self.step_count / self.cfg.epsilon_decay)
        if random.random() < self.epsilon:
            return random.randint(0, self.cfg.action_dim - 1)
        with torch.no_grad():
            q = self.policy_net(torch.FloatTensor(state).unsqueeze(0))
        return q.argmax().item()

    def update(self):
        if len(self.buffer) < self.cfg.batch_size:
            return 0.0

        states, actions, rewards, next_states, dones = self.buffer.sample(self.cfg.batch_size)
        s = torch.FloatTensor(states)
        a = torch.LongTensor(actions).unsqueeze(1)
        r = torch.FloatTensor(rewards).unsqueeze(1)
        ns = torch.FloatTensor(next_states)
        d = torch.FloatTensor(dones).unsqueeze(1)

        q_curr = self.policy_net(s).gather(1, a)
        with torch.no_grad():
            q_next = self.target_net(ns).max(1, keepdim=True)[0]
            q_target = r + self.cfg.gamma * q_next * (1 - d)

        loss = F.smooth_l1_loss(q_curr, q_target)
        self.opt.zero_grad()
        loss.backward()
        nn.utils.clip_grad_norm_(self.policy_net.parameters(), 10.0)
        self.opt.step()

        self.step_count += 1
        if self.step_count % self.cfg.target_update_freq == 0:
            self.target_net.load_state_dict(self.policy_net.state_dict())

        return loss.item()


# ─── A3C Global Actor ─────────────────────────────────────────────────────────


class A3CGlobalActor(nn.Module):
    """Shared actor-critic network for A3C. Workers compute local gradients
    and apply them to this shared network."""

    def __init__(self, state_dim: int, action_dim: int):
        super().__init__()
        self.shared = nn.Sequential(
            nn.Linear(state_dim, 128),
            nn.ReLU(),
            nn.Linear(128, 64),
            nn.ReLU(),
        )
        self.policy_head = nn.Linear(64, action_dim)
        self.value_head = nn.Linear(64, 1)
        self._lock = threading.Lock()

    def forward(self, x: torch.Tensor):
        h = self.shared(x)
        return F.softmax(self.policy_head(h), dim=-1), self.value_head(h).squeeze(-1)

    def apply_gradients(self, local_grads: list):
        """Thread-safe gradient application from A3C workers."""
        with self._lock:
            for param, grad in zip(self.parameters(), local_grads):
                if param.grad is None:
                    param.grad = grad.clone()
                else:
                    param.grad += grad


class A3CWorker(threading.Thread):
    """One A3C worker thread per service group."""

    def __init__(self, worker_id: int, global_actor: A3CGlobalActor, cfg: DQNConfig, env: "RateLimitEnv"):
        super().__init__(daemon=True)
        self.worker_id = worker_id
        self.global_actor = global_actor
        # deepcopy cannot pickle the threading.Lock inside A3CGlobalActor._lock;
        # create a fresh local actor with the same architecture and copy weights.
        obs_dim = global_actor.shared[0].in_features
        act_dim = global_actor.policy_head.out_features
        self.local_actor = A3CGlobalActor(obs_dim, act_dim)
        self.local_actor.load_state_dict(global_actor.state_dict())
        self.cfg = cfg
        self.env = env
        self.opt = Adam(global_actor.parameters(), lr=cfg.lr)
        self.total_steps = 0

    def run(self):
        state = self.env.reset()
        while self.total_steps < self.cfg.total_steps:
            # Sync local with global
            self.local_actor.load_state_dict(self.global_actor.state_dict())

            states, actions, rewards, values = [], [], [], []
            done = False
            for _ in range(20):  # n-step return
                s_t = torch.FloatTensor(state).unsqueeze(0)
                with torch.no_grad():
                    probs, value = self.local_actor(s_t)
                dist = torch.distributions.Categorical(probs)
                action = dist.sample().item()

                next_state, reward, done = self.env.step(action)
                states.append(state)
                actions.append(action)
                rewards.append(reward)
                values.append(value.item())
                state = next_state
                self.total_steps += 1
                if done:
                    state = self.env.reset()
                    break

            # Compute n-step returns
            R = 0.0 if done else values[-1]
            returns = []
            for r in reversed(rewards):
                R = r + self.cfg.gamma * R
                returns.insert(0, R)

            # Compute local gradients
            s_t = torch.FloatTensor(np.array(states))
            a_t = torch.LongTensor(actions)
            ret_t = torch.FloatTensor(returns)

            probs, vals = self.local_actor(s_t)
            dist = torch.distributions.Categorical(probs)
            log_probs = dist.log_prob(a_t)
            advantages = ret_t - vals.detach()

            actor_loss = -(log_probs * advantages).mean()
            critic_loss = F.mse_loss(vals, ret_t)
            entropy = dist.entropy().mean()
            loss = actor_loss + 0.5 * critic_loss - 0.01 * entropy

            # Compute grads on local model
            self.local_actor.zero_grad()
            loss.backward()
            grads = [p.grad for p in self.local_actor.parameters()]
            self.global_actor.apply_gradients(grads)
            self.opt.step()


# ─── Rate Limit Simulation Environment ────────────────────────────────────────


class RateLimitEnv:
    def __init__(self, service_id: int, rng_seed: int = 0):
        self.rng = np.random.default_rng(rng_seed)
        self.service_id = service_id
        self.current_limit = 1000.0
        self.min_rps = 100.0
        self.max_rps = 10000.0
        self.step_n = 0

    def reset(self) -> np.ndarray:
        self.current_limit = 1000.0
        self.step_n = 0
        return self._observe()

    def step(self, action: int) -> tuple[np.ndarray, float, bool]:
        # Apply action
        if action == 0:
            self.current_limit = max(self.min_rps, self.current_limit * 0.90)
        elif action == 2:
            self.current_limit = min(self.max_rps, self.current_limit * 1.10)

        # Simulate environment response
        true_rps = 800 + 400 * np.sin(self.step_n / 200)
        cpu_pct = min(1.0, true_rps / (self.current_limit + 1))
        error_r = max(0, cpu_pct - 0.85) * 2

        reward = (
            min(self.current_limit, true_rps) / 100  # throughput
            - error_r * 100  # penalise errors
            - max(0, cpu_pct - 0.90) * 1e9
        )  # overload penalty

        self.step_n += 1
        done = self.step_n >= 1000
        return self._observe(), reward, done

    def _observe(self) -> np.ndarray:
        true_rps = 800 + 400 * np.sin(self.step_n / 200)
        cpu_pct = min(1.0, true_rps / (self.current_limit + 1))
        return np.array(
            [
                true_rps / 10000,
                cpu_pct,
                0.0,  # queue (simplified)
                max(0, cpu_pct - 0.85),  # error rate
                cpu_pct * 200,  # p99 estimate (ms)
                self.current_limit / 10000,
            ],
            dtype=np.float32,
        )


# ─── Training Entry Point ─────────────────────────────────────────────────────


def train(cfg: Optional[DQNConfig] = None, output_dir: str = "models"):
    import os

    if cfg is None:
        cfg = DQNConfig(num_services=4)
    os.makedirs(output_dir, exist_ok=True)

    print(f"Training DQN+A3C rate limiter | services={cfg.num_services}")

    # ── Per-service DQN agents ────────────────────────────────────────────────
    dqn_agents = [DQNServiceAgent(i, cfg) for i in range(cfg.num_services)]
    dqn_envs = [RateLimitEnv(i, rng_seed=i) for i in range(cfg.num_services)]
    states = [env.reset() for env in dqn_envs]

    # ── Shared A3C global actor ───────────────────────────────────────────────
    global_actor = A3CGlobalActor(cfg.state_dim, cfg.action_dim)
    workers = [A3CWorker(i, global_actor, cfg, RateLimitEnv(i, rng_seed=100 + i)) for i in range(cfg.num_services)]
    for w in workers:
        w.start()

    # ── DQN main loop ─────────────────────────────────────────────────────────
    for step in range(cfg.total_steps):
        for i, (agent, env) in enumerate(zip(dqn_agents, dqn_envs)):
            action = agent.select_action(states[i])
            next_state, reward, done = env.step(action)
            agent.buffer.push(states[i], action, reward, next_state, float(done))
            states[i] = next_state if not done else env.reset()
            agent.update()

        if step % 10000 == 0:
            print(f"  DQN step={step}")

    # ── Export ────────────────────────────────────────────────────────────────
    for i, agent in enumerate(dqn_agents):
        torch.save(agent.policy_net.state_dict(), f"{output_dir}/dqn_service_{i}.pt")

    torch.save(global_actor.state_dict(), f"{output_dir}/a3c_global.pt")

    dummy = torch.zeros(1, cfg.state_dim)
    torch.onnx.export(
        dqn_agents[0].policy_net,
        dummy,
        f"{output_dir}/dqn_rate_limiter.onnx",
        input_names=["state"],
        output_names=["q_values"],
        opset_version=17,
    )
    print(f"DQN+A3C models saved to {output_dir}/")


if __name__ == "__main__":
    train()
