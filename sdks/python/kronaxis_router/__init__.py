"""Kronaxis Router Python SDK.

Wraps the OpenAI Python client to automatically route requests through
Kronaxis Router with cost-optimised backend selection.

Usage:
    from kronaxis_router import KronaxisRouter

    router = KronaxisRouter("http://localhost:8050")
    response = router.chat("Summarise this document...", tier=2)
"""

from .client import KronaxisRouter, Tier

__version__ = "0.1.0"
__all__ = ["KronaxisRouter", "Tier"]
