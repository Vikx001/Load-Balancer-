"""
ml.simulation — simulation environment for Omega-LB offline training.

Exports
-------
LBSimEnv
    M/M/1 queue simulation used for PPO/DQN offline training and testing.
SimConfig
    Dataclass with all environment tuning knobs.

Quick start::

    from ml.simulation import LBSimEnv

    env = LBSimEnv(n_backends=4)
    state, _ = env.reset()
    weights   = [0.25, 0.25, 0.25, 0.25]
    state, r, terminated, truncated, info = env.step(weights)
"""

from .lb_env import LBSimEnv, SimConfig

__all__ = ["LBSimEnv", "SimConfig"]
