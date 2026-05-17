"""
ml.kan — KAN inference package for Omega-LB Layer 3.

Exports
-------
KANInference
    Thread-safe routing weight inference with ONNX + symbolic fallback.

Quick start::

    from ml.kan import KANInference
    import numpy as np

    kan = KANInference.load("ml/models/kan_actor.onnx")  # symbolic if not found
    weights = kan.infer(
        cpu    = np.array([0.4, 0.6, 0.2]),
        lat_ms = np.array([45., 55., 120.]),
        err    = np.array([0.001, 0.002, 0.01]),
        health = [True, True, True],
    )
"""

from .kan_inference import KANInference

__all__ = ["KANInference"]
