"""Server-side cancellation and deadline checkpoints for gRPC handlers.

gRPC Python cannot interrupt a blocking library call. Long loops therefore
call :func:`check_rpc` between units of work (pages, blocks, layout lines)
so a client deadline/cancellation or the server-side cap releases the thread
promptly after the current unit finishes.
"""
from __future__ import annotations

import time
from collections.abc import Callable

import grpc

# Checkpoint callables raise grpc.RpcError via context.abort() when the RPC
# must stop. ``None`` means "no checkpoint requested".
RpcCheckpoint = Callable[[], None] | None


def check_rpc(context, started_at: float, max_seconds: float) -> None:
    """Abort the RPC when it is cancelled, out of time, or over the cap.

    The client-supplied deadline (``time_remaining``) is authoritative; the
    server cap is a backstop for callers that omit one.
    """
    try:
        remaining = context.time_remaining()
    except Exception:
        remaining = None
    if remaining is not None and remaining <= 0:
        context.abort(grpc.StatusCode.DEADLINE_EXCEEDED, "RPC deadline exceeded")
    if max_seconds > 0 and time.monotonic() - started_at > max_seconds:
        context.abort(
            grpc.StatusCode.DEADLINE_EXCEEDED,
            "RPC exceeded the parser server-side time limit",
        )
    if not context.is_active():
        context.abort(grpc.StatusCode.CANCELLED, "RPC was cancelled")
