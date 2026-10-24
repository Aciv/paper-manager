"""Private worker IPC and process cleanup without heavy parser dependencies.

Unix retains its local socket. Windows uses authenticated loopback TCP because
CPython on Windows does not provide AF_UNIX. The endpoint file contains no key;
the random per-worker key is inherited through the child's environment.
"""
from __future__ import annotations

import hmac
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import threading

TOKEN_ENV = "PAPERS_PARSER_WORKER_TOKEN"
PARENT_ENV = "PAPERS_PARSER_OWNED_WORKER"
_TCP = os.name == "nt"


def await_parent() -> None:
    """Hold imports until Windows ownership is established; detect parent death."""
    if os.environ.get(PARENT_ENV) != "1":
        return
    if sys.stdin.buffer.read(1) != b"\x01":
        raise SystemExit("worker parent handshake failed")

    def watch_parent():
        # The pipe has one writer: the owning server. EOF also covers crashes.
        sys.stdin.buffer.read()
        if os.name != "nt":
            # start_process created this private session. Clear native-library
            # helpers too if the owning server died before normal cleanup.
            os.killpg(os.getpgrp(), signal.SIGKILL)
        os._exit(0)

    threading.Thread(target=watch_parent, name="parser-worker-parent", daemon=True).start()


def _windows_job(process: subprocess.Popen):
    """Attach a kill-on-close Job Object before the child loads native libs."""
    import ctypes
    from ctypes import wintypes

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.CreateJobObjectW.argtypes = [ctypes.c_void_p, wintypes.LPCWSTR]
    kernel.CreateJobObjectW.restype = wintypes.HANDLE
    kernel.SetInformationJobObject.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD]
    kernel.AssignProcessToJobObject.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]

    class BasicLimits(ctypes.Structure):
        _fields_ = [("per_process_time", ctypes.c_int64), ("per_job_time", ctypes.c_int64),
                    ("flags", wintypes.DWORD), ("min_working_set", ctypes.c_size_t),
                    ("max_working_set", ctypes.c_size_t), ("active_process_limit", wintypes.DWORD),
                    ("affinity", ctypes.c_size_t), ("priority", wintypes.DWORD),
                    ("scheduling", wintypes.DWORD)]

    class IO(ctypes.Structure):
        _fields_ = [(name, ctypes.c_uint64) for name in
                    ("read_ops", "write_ops", "other_ops", "read_bytes", "write_bytes", "other_bytes")]

    class ExtendedLimits(ctypes.Structure):
        _fields_ = [("basic", BasicLimits), ("io", IO),
                    ("process_memory", ctypes.c_size_t), ("job_memory", ctypes.c_size_t),
                    ("peak_process_memory", ctypes.c_size_t), ("peak_job_memory", ctypes.c_size_t)]

    handle = kernel.CreateJobObjectW(None, None)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    limits = ExtendedLimits()
    limits.basic.flags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    if not kernel.SetInformationJobObject(handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)) or not kernel.AssignProcessToJobObject(handle, int(process._handle)):
        error = ctypes.WinError(ctypes.get_last_error())
        kernel.CloseHandle(handle)
        raise error
    return kernel, handle


def start_process(command: list[str], *, env: dict, cwd: str) -> subprocess.Popen:
    env = {**env, PARENT_ENV: "1"}
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, close_fds=True,
                               start_new_session=os.name != "nt", env=env, cwd=cwd)
    try:
        if os.name == "nt":
            process._parser_job = _windows_job(process)
        process.stdin.write(b"\x01")
        process.stdin.flush()
        return process
    except BaseException:
        stop_process(process)
        raise


def listen(path: Path, backlog: int) -> socket.socket:
    listener = socket.socket(socket.AF_INET if _TCP else socket.AF_UNIX,
                             socket.SOCK_STREAM)
    try:
        if _TCP:
            listener.bind(("127.0.0.1", 0))
        else:
            path.unlink(missing_ok=True)
            listener.bind(str(path))
            os.chmod(path, 0o600)
        listener.listen(backlog)
        if _TCP:
            # Publish only after listen succeeds; parent retries partial reads.
            path.write_text(json.dumps({"port": listener.getsockname()[1]}), encoding="utf-8")
        return listener
    except BaseException:
        listener.close()
        raise


def connect(path: Path, token: str, timeout: float = 0.5) -> socket.socket:
    connection = socket.socket(socket.AF_INET if _TCP else socket.AF_UNIX,
                               socket.SOCK_STREAM)
    connection.settimeout(timeout)
    try:
        if _TCP:
            try:
                port = int(json.loads(path.read_text(encoding="utf-8"))["port"])
            except (OSError, ValueError, KeyError) as exc:
                raise OSError("worker endpoint is not ready") from exc
            connection.connect(("127.0.0.1", port))
            connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            connection.sendall(token.encode("ascii"))
            if connection.recv(1) != b"\x01":
                raise OSError("worker authentication failed")
        else:
            connection.connect(str(path))
        return connection
    except BaseException:
        connection.close()
        raise


def authenticate(connection: socket.socket) -> bool:
    if not _TCP:
        return True
    connection.settimeout(0.5)
    connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    expected = os.environ.get(TOKEN_ENV, "").encode("ascii")
    if len(expected) != 64:
        return False
    received = bytearray()
    try:
        while len(received) < len(expected):
            part = connection.recv(len(expected) - len(received))
            if not part:
                return False
            received.extend(part)
        if not hmac.compare_digest(received, expected):
            return False
        connection.sendall(b"\x01")
        return True
    except OSError:
        return False


def stop_process(process: subprocess.Popen) -> None:
    """Reap only this owned worker, including native-library descendants."""
    try:
        if process.stdin is not None:
            try:
                process.stdin.close()
            except BrokenPipeError:
                pass
        job = getattr(process, "_parser_job", None)
        if job is not None:
            kernel, handle = job
            kernel.CloseHandle(handle)
            process._parser_job = None
        if process.poll() is not None:
            return
        if os.name == "nt":
            # SIGTERM on Windows is TerminateProcess and does not clean a tree.
            subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                           timeout=5, check=False)
        else:
            os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=5)
    except (ProcessLookupError, PermissionError):
        # Even if the EOF watcher won the signal race, reap the owned child.
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            pass
    except subprocess.TimeoutExpired:
        if os.name == "nt":
            process.kill()
        else:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        process.wait(timeout=2)
