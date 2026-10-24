"""Local worker runtime over a private endpoint, with request-scoped threads.

The server process owns process lifecycle and scheduling. This module stays
free of PyMuPDF/ONNX/CRF imports; the kind-specific worker module supplies the
heavy handler after imports have been selected.
"""
from __future__ import annotations

import os
import select
import signal
import socket
import threading
import time
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import grpc

from logger import get_logger
from papers_parser.worker_transport import authenticate, listen
from papers_parser.worker_protocol import (
    STATUS_ERROR,
    STATUS_OK,
    WorkerCancelled,
    WorkerProtocolError,
    encode_error,
    read_frame,
    write_frame,
)

LOG = get_logger(__name__)


class WorkerAbort(Exception):
    """A typed failure that should become a gRPC status in the server."""

    def __init__(self, code: grpc.StatusCode | str, message: str):
        code_name = code.name if isinstance(code, grpc.StatusCode) else str(code)
        super().__init__(message)
        self.code = code_name
        self.message = str(message)[:8192]


class WorkerContext:
    """The small gRPC-context surface used by existing parser services."""

    def __init__(self, cancelled: threading.Event):
        self._cancelled = cancelled

    def abort(self, code, message) -> None:
        raise WorkerAbort(code, message)

    def time_remaining(self):
        # The server enforces the client deadline by cancelling the worker.
        # The service's own rpc_max_seconds remains the server-side backstop.
        return None

    def is_active(self) -> bool:
        return not self._cancelled.is_set()


def serve_worker(
    *,
    socket_path: Path,
    expected_method: int,
    handler: Callable[[bytes, WorkerContext], bytes],
    idle_seconds: float,
    max_jobs: int,
    max_payload_bytes: int,
) -> None:
    """Bind the private socket, run jobs, and exit on idle/shutdown."""
    cancelled = threading.Event()

    def request_cancel(_signum, _frame) -> None:
        cancelled.set()

    if hasattr(signal, "SIGUSR1"):
        signal.signal(signal.SIGUSR1, request_cancel)

    def stop_worker(_signum, _frame) -> None:
        raise SystemExit(0)

    signal.signal(signal.SIGTERM, stop_worker)
    if socket_path.exists():
        socket_path.unlink()
    listener = listen(socket_path, 1)
    LOG.info("parser worker ready kind=%s socket=%s", expected_method, socket_path)
    jobs = 0
    try:
        while True:
            connection, _ = listener.accept()
            if authenticate(connection):
                break
            connection.close()
        with connection:
            connection.settimeout(0.5)
            while jobs < max_jobs:
                cancelled.clear()
                deadline = time.monotonic() + max(idle_seconds, 1.0)
                try:
                    method, payload = read_frame(
                        connection,
                        max_payload_bytes=max_payload_bytes,
                        deadline=deadline,
                        clock=time.monotonic,
                    )
                except TimeoutError:
                    LOG.info("parser worker idle timeout; exiting")
                    break
                except WorkerProtocolError:
                    LOG.warning("parser worker connection closed")
                    break
                except WorkerCancelled:
                    write_frame(connection, STATUS_ERROR, encode_error("CANCELLED", "request cancelled"))
                    continue
                if method != expected_method:
                    write_frame(
                        connection,
                        STATUS_ERROR,
                        encode_error("INVALID_ARGUMENT", "worker received the wrong method"),
                    )
                    continue
                try:
                    response = handler(payload, WorkerContext(cancelled))
                    write_frame(connection, STATUS_OK, response)
                    jobs += 1
                except WorkerAbort as exc:
                    LOG.warning("worker request failed code=%s message=%s", exc.code, exc.message)
                    try:
                        write_frame(
                            connection,
                            STATUS_ERROR,
                            encode_error(exc.code, exc.message),
                        )
                    except OSError:
                        break
                except Exception:
                    LOG.exception("parser worker request failed unexpectedly")
                    try:
                        write_frame(
                            connection,
                            STATUS_ERROR,
                            encode_error("INTERNAL", "parser worker failed"),
                        )
                    except OSError:
                        break
                    break
    finally:
        listener.close()
        try:
            socket_path.unlink()
        except FileNotFoundError:
            pass
        LOG.info("parser worker stopped jobs=%d", jobs)


def serve_shared_worker(
    *,
    socket_path: Path,
    expected_method: int,
    handler: Callable[[bytes, WorkerContext], bytes],
    idle_seconds: float,
    max_jobs: int,
    max_payload_bytes: int,
    concurrency: int,
) -> None:
    """Serve multiple concurrent jobs from one heavy worker process.

    The server opens one connection per call. Each connection is handled in a
    thread, so the heavy imports/model are loaded once and shared by all
    in-flight jobs. The process exits after the idle window or max_jobs.
    """
    stop = threading.Event()

    def request_stop(_signum, _frame) -> None:
        stop.set()

    signal.signal(signal.SIGTERM, request_stop)
    signal.signal(signal.SIGINT, request_stop)
    if hasattr(signal, "SIGUSR1"):
        # Per-connection cancellation is detected from socket EOF; a process
        # signal would cancel every concurrent job.
        signal.signal(signal.SIGUSR1, lambda *_: None)

    if socket_path.exists():
        socket_path.unlink()
    listener = listen(socket_path, max(16, concurrency * 4))
    listener.settimeout(0.5)

    lock = threading.Lock()
    state = {"active": 0, "jobs": 0, "last_activity": time.monotonic()}
    connections: set[socket.socket] = set()
    executor = ThreadPoolExecutor(max_workers=concurrency, thread_name_prefix="parser-worker-request")

    def handle_connection(connection: socket.socket) -> None:
        try:
            if not authenticate(connection):
                return
            connection.settimeout(0.5)
            deadline = time.monotonic() + 60
            try:
                method, payload = read_frame(
                    connection,
                    max_payload_bytes=max_payload_bytes,
                    deadline=deadline,
                    clock=time.monotonic,
                )
            except (TimeoutError, WorkerProtocolError):
                return
            if method != expected_method:
                try:
                    write_frame(
                        connection,
                        STATUS_ERROR,
                        encode_error("INVALID_ARGUMENT", "worker received the wrong method"),
                    )
                except OSError:
                    pass
                return

            cancelled = threading.Event()
            done = threading.Event()

            def watch_disconnect() -> None:
                # The server cancels one call by closing its connection. Poll
                # for EOF without consuming response bytes.
                while not done.is_set() and not cancelled.is_set():
                    try:
                        readable, _, _ = select.select([connection], [], [], 0.05)
                    except (OSError, ValueError):
                        cancelled.set()
                        return
                    if readable:
                        try:
                            probe = connection.recv(1, socket.MSG_PEEK)
                        except OSError:
                            cancelled.set()
                            return
                        if not probe:
                            cancelled.set()
                            return

            watcher = threading.Thread(
                target=watch_disconnect,
                name="parser-worker-cancel-watch",
                daemon=True,
            )
            watcher.start()
            try:
                response = handler(payload, WorkerContext(cancelled))
                done.set()
                watcher.join(timeout=0.6)
                write_frame(connection, STATUS_OK, response)
                with lock:
                    state["jobs"] += 1
                    state["last_activity"] = time.monotonic()
            except WorkerAbort as exc:
                LOG.warning("worker request failed code=%s message=%s", exc.code, exc.message)
                done.set()
                watcher.join(timeout=0.6)
                try:
                    write_frame(
                        connection,
                        STATUS_ERROR,
                        encode_error(exc.code, exc.message),
                    )
                except OSError:
                    pass
            except Exception:
                LOG.exception("parser worker request failed unexpectedly")
                done.set()
                watcher.join(timeout=0.6)
                try:
                    write_frame(
                        connection,
                        STATUS_ERROR,
                        encode_error("INTERNAL", "parser worker failed"),
                    )
                except OSError:
                    pass
        finally:
            with lock:
                connections.discard(connection)
                state["active"] = max(0, state["active"] - 1)
                state["last_activity"] = time.monotonic()
            try:
                connection.close()
            except OSError:
                pass

    LOG.info(
        "parser worker ready kind=%s socket=%s concurrency=%d",
        expected_method,
        socket_path,
        concurrency,
    )
    try:
        while not stop.is_set():
            with lock:
                if state["jobs"] >= max_jobs and state["active"] == 0:
                    break
                if (
                    state["active"] == 0
                    and time.monotonic() - state["last_activity"] >= idle_seconds
                ):
                    LOG.info("parser worker idle timeout; exiting")
                    break
            try:
                connection, _ = listener.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            with lock:
                # A timed-out client may leave a native call finishing in its
                # thread. Bound both execution and accepted backlog inside the
                # worker, even after the parent releases its admission slot.
                if state["active"] >= concurrency * 2:
                    connection.close()
                    continue
                connections.add(connection)
                state["active"] += 1
                state["last_activity"] = time.monotonic()
            executor.submit(handle_connection, connection)
    finally:
        listener.close()
        executor.shutdown(wait=False, cancel_futures=True)
        with lock:
            remaining = list(connections)
        for connection in remaining:
            try:
                connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            connection.close()
        try:
            socket_path.unlink()
        except FileNotFoundError:
            pass
        with lock:
            completed = state["jobs"]
        LOG.info("parser worker stopped jobs=%d", completed)
