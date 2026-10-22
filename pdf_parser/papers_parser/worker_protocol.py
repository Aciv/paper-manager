"""Small framed protocol between the lightweight parser server and workers.

The wire format is intentionally local and private:

    request:  method:uint8 | payload_length:uint32 | protobuf payload
    response: status:uint8 | payload_length:uint32 | protobuf payload / JSON error

It is not a public API. The external gRPC contract remains the source of truth;
workers only execute one unary request at a time.
"""
from __future__ import annotations

import json
import socket
import struct
from collections.abc import Callable

METHOD_RECOGNIZE = 1
METHOD_EXTRACT = 2
METHOD_RENDER = 3

STATUS_OK = 0
STATUS_ERROR = 1

FRAME_HEADER = struct.Struct("!BI")
DEFAULT_SOCKET_POLL_SECONDS = 0.5


class WorkerProtocolError(Exception):
    """The local worker connection is malformed or closed."""


class WorkerCancelled(Exception):
    """The caller's gRPC request is no longer active."""


def encode_frame(kind: int, payload: bytes) -> bytes:
    if kind < 0 or kind > 255:
        raise ValueError("frame kind must fit in one byte")
    if len(payload) > 0xFFFFFFFF:
        raise ValueError("frame payload is too large")
    return FRAME_HEADER.pack(kind, len(payload)) + payload


def encode_error(code: str, message: str) -> bytes:
    return json.dumps(
        {"code": str(code), "message": str(message)[:8192]},
        ensure_ascii=False,
    ).encode("utf-8")


def decode_error(payload: bytes) -> tuple[str, str]:
    try:
        decoded = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise WorkerProtocolError("worker returned a malformed error") from exc
    code = str(decoded.get("code") or "INTERNAL")
    message = str(decoded.get("message") or "worker failed")
    return code, message[:8192]


def recv_exact(
    sock: socket.socket,
    size: int,
    *,
    cancel_check: Callable[[], bool] | None = None,
    deadline: float | None = None,
    clock: Callable[[], float] | None = None,
) -> bytes:
    """Read exactly ``size`` bytes, polling cancellation/deadline while idle."""
    if size < 0:
        raise WorkerProtocolError("negative frame size")
    if size == 0:
        return b""
    chunks: list[bytes] = []
    remaining = size
    while remaining:
        try:
            chunk = sock.recv(remaining)
        except socket.timeout:
            if deadline is not None and clock is not None and clock() > deadline:
                raise TimeoutError("worker response timed out")
            if cancel_check is not None and cancel_check():
                raise WorkerCancelled("worker request was cancelled")
            continue
        if not chunk:
            raise WorkerProtocolError("worker connection closed")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def read_frame(
    sock: socket.socket,
    *,
    max_payload_bytes: int,
    cancel_check: Callable[[], bool] | None = None,
    deadline: float | None = None,
    clock: Callable[[], float] | None = None,
) -> tuple[int, bytes]:
    header = recv_exact(
        sock,
        FRAME_HEADER.size,
        cancel_check=cancel_check,
        deadline=deadline,
        clock=clock,
    )
    kind, length = FRAME_HEADER.unpack(header)
    if length > max_payload_bytes:
        raise WorkerProtocolError("worker frame exceeds the configured limit")
    payload = recv_exact(
        sock,
        length,
        cancel_check=cancel_check,
        deadline=deadline,
        clock=clock,
    )
    return kind, payload


def write_frame(sock: socket.socket, kind: int, payload: bytes) -> None:
    sock.sendall(encode_frame(kind, payload))
