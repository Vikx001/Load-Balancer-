"""
ml.cbf — CBF safety projection package for Omega-LB Layer 2.

Exports
-------
CBFProjector
    Gradient-descent CBF projection.  Returns (safe_weights, cbf_fired).
SafetyMonitor
    Wraps CBFProjector with violation tracking, rolling stats, and audit log.

Quick start::

    from ml.cbf import CBFProjector, SafetyMonitor
    import numpy as np

    cbf = CBFProjector(cap=0.80, lam=0.5)
    weights, fired = cbf.project(
        np.array([0.6, 0.2, 0.1, 0.1]),
        np.array([0.9, 0.3, 0.2, 0.4]),  # backend-0 near cap
    )
"""

from .cbf_runtime import CBFProjector, SafetyMonitor, CBFResult, CBFViolation

__all__ = ["CBFProjector", "SafetyMonitor", "CBFResult", "CBFViolation"]
