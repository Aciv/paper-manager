"""Bounded parser worker pool owned by the lightweight gRPC server."""
from __future__ import annotations

import base64
import json
import os
import shutil
import socket
import signal
import subprocess
import sys
import tempfile
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from logger import get_logger
from papers_parser.worker_protocol import (
    STATUS_ERROR,
    WorkerCancelled,
    WorkerProtocolError,
    decode_error,
    read_frame,
    write_frame,
)

LOG = get_logger(__name__)


class WorkerUnavailable(Exception):
    """No worker could be started or the active worker died."""


class WorkerQueueFull(Exception):
    """The worker pool is saturated beyond its admission timeout."""


class WorkerRPCError(Exception):
    """A typed error returned by a worker."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code
        self.message = message


@dataclass(frozen=True)
class WorkerPoolConfig:
    kind: str
    max_workers: int
    idle_seconds: float
    max_jobs: int
    queue_timeout: float
    start_timeout: float
    max_payload_bytes: int
    worker_config: dict
    # Shared-worker mode: one process, multiple concurrent connections/threads.
    concurrency: int = 1


class WorkerHandle:
    def __init__(
        self,
        *,
        kind: str,
        process: subprocess.Popen,
        connection: socket.socket,
        socket_path: Path,
        temp_dir: Path,
        max_jobs: int,
        max_payload_bytes: int,
    ):
        self.kind = kind
        self.process = process
        self.connection = connection
        self.socket_path = socket_path
        self.temp_dir = temp_dir
        self.max_jobs = max_jobs
        self.max_payload_bytes = max_payload_bytes
        self.jobs = 0
        self.last_used = time.monotonic()
        self._call_lock = threading.Lock()
        self._closed = False

    @property
    def alive(self) -> bool:
        return not self._closed and self.process.poll() is None

    def call(
        self,
        method: int,
        payload: bytes,
        *,
        cancel_check: Callable[[], bool] | None,
        timeout: float,
    ) -> bytes:
        with self._call_lock:
            if not self.alive:
                raise WorkerUnavailable("worker is not running")
            try:
                write_frame(self.connection, method, payload)
                status, response = read_frame(
                    self.connection,
                    max_payload_bytes=self.max_payload_bytes,
                    cancel_check=cancel_check,
                    deadline=time.monotonic() + timeout,
                    clock=time.monotonic,
                )
            except WorkerCancelled as exc:
                self._signal_cancel()
                self.close()
                raise WorkerRPCError("CANCELLED", "parser request was cancelled") from exc
            except TimeoutError as exc:
                self.close()
                raise WorkerUnavailable("worker response timed out") from exc
            except (OSError, WorkerProtocolError) as exc:
                self.close()
                raise WorkerUnavailable("worker connection failed") from exc
            if status == STATUS_ERROR:
                code, message = decode_error(response)
                raise WorkerRPCError(code, message)
            self.jobs += 1
            self.last_used = time.monotonic()
            return response

    def _signal_cancel(self) -> None:
        if not self.alive or not hasattr(signal, "SIGUSR1"):
            return
        try:
            os.kill(self.process.pid, signal.SIGUSR1)
        except ProcessLookupError:
            pass

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            self.connection.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self.connection.close()
        except OSError:
            pass
        try:
            if self.process.poll() is None:
                os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(os.getpgid(self.process.pid), signal.SIGKILL)
                    try:
                        self.process.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        pass
        except (ProcessLookupError, PermissionError):
            pass
        try:
            shutil.rmtree(self.temp_dir, ignore_errors=True)
        except OSError:
            pass


class WorkerPool:
    def __init__(self, config: WorkerPoolConfig):
        if config.max_workers <= 0:
            raise ValueError("max_workers must be positive")
        if config.idle_seconds <= 0 or config.queue_timeout < 0 or config.start_timeout <= 0:
            raise ValueError("worker pool timing values are invalid")
        self.config = config
        self._condition = threading.Condition()
        self._workers: set[WorkerHandle] = set()
        self._idle: list[WorkerHandle] = []
        self._starting = 0
        self._closed = False
        self._monitor = threading.Thread(
            target=self._monitor_idle,
            name=f"parser-{config.kind}-worker-monitor",
            daemon=True,
        )
        self._monitor.start()

    @property
    def worker_count(self) -> int:
        with self._condition:
            return len(self._workers)

    def call(
        self,
        method: int,
        payload: bytes,
        *,
        cancel_check: Callable[[], bool] | None = None,
        timeout: float,
    ) -> bytes:
        worker = self._acquire(cancel_check)
        try:
            response = worker.call(
                method,
                payload,
                cancel_check=cancel_check,
                timeout=timeout,
            )
        except WorkerRPCError:
            self._retire(worker)
            raise
        except Exception:
            self._retire(worker)
            raise
        if worker.jobs >= self.config.max_jobs or not worker.alive:
            self._retire(worker)
        else:
            with self._condition:
                if not self._closed and worker.alive:
                    worker.last_used = time.monotonic()
                    self._idle.append(worker)
                    self._condition.notify()
                else:
                    self._retire_locked(worker)
        return response

    def _acquire(self, cancel_check: Callable[[], bool] | None = None) -> WorkerHandle:
        deadline = time.monotonic() + self.config.queue_timeout
        while True:
            with self._condition:
                if self._closed:
                    raise WorkerUnavailable("worker pool is closed")
                if cancel_check is not None and cancel_check():
                    raise WorkerRPCError("CANCELLED", "parser request was cancelled")
                while self._idle:
                    worker = self._idle.pop()
                    if worker.alive:
                        return worker
                    self._workers.discard(worker)
                    worker.close()
                if len(self._workers) + self._starting < self.config.max_workers:
                    self._starting += 1
                    break
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise WorkerQueueFull("parser worker pool is saturated")
                self._condition.wait(min(remaining, 0.5))
        try:
            worker = self._spawn()
        except Exception:
            with self._condition:
                self._starting -= 1
                self._condition.notify_all()
            raise
        with self._condition:
            self._starting -= 1
            if self._closed:
                self._condition.notify_all()
            else:
                self._workers.add(worker)
                self._condition.notify_all()
                return worker
        worker.close()
        raise WorkerUnavailable("worker pool closed during startup")

    def _spawn(self) -> WorkerHandle:
        temp_dir = Path(tempfile.mkdtemp(prefix=f"papers-parser-{self.config.kind}-"))
        socket_path = temp_dir / "worker.sock"
        encoded = base64.b64encode(
            json.dumps(self.config.worker_config, separators=(",", ":")).encode("utf-8")
        ).decode("ascii")
        command = [
            sys.executable,
            "-m",
            "papers_parser.worker",
            "--kind",
            self.config.kind,
            "--socket",
            str(socket_path),
            "--config",
            encoded,
            "--idle-seconds",
            str(max(self.config.idle_seconds * 2, 5)),
            "--max-jobs",
            str(self.config.max_jobs),
            "--concurrency",
            str(self.config.concurrency),
        ]
        env = os.environ.copy()
        env.setdefault("PYTHONUNBUFFERED", "1")
        process = subprocess.Popen(
            command,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
            env=env,
        )
        threading.Thread(
            target=self._drain_worker_output,
            args=(process, self.config.kind),
            name=f"parser-{self.config.kind}-worker-log",
            daemon=True,
        ).start()
        connection = None
        try:
            deadline = time.monotonic() + self.config.start_timeout
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise WorkerUnavailable("parser worker exited during startup")
                try:
                    connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                    connection.settimeout(0.5)
                    connection.connect(str(socket_path))
                    connection.settimeout(0.5)
                    break
                except OSError:
                    if connection is not None:
                        connection.close()
                    connection = None
                    time.sleep(0.05)
            if connection is None:
                raise WorkerUnavailable("parser worker startup timed out")
        except Exception:
            try:
                if process.poll() is None:
                    os.killpg(os.getpgid(process.pid), signal.SIGTERM)
                    process.wait(timeout=5)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                except ProcessLookupError:
                    pass
            shutil.rmtree(temp_dir, ignore_errors=True)
            raise
        return WorkerHandle(
            kind=self.config.kind,
            process=process,
            connection=connection,
            socket_path=socket_path,
            temp_dir=temp_dir,
            max_jobs=self.config.max_jobs,
            max_payload_bytes=self.config.max_payload_bytes,
        )

    def _retire(self, worker: WorkerHandle) -> None:
        with self._condition:
            self._retire_locked(worker)

    def _retire_locked(self, worker: WorkerHandle) -> None:
        self._workers.discard(worker)
        if worker in self._idle:
            self._idle.remove(worker)
        worker.close()
        self._condition.notify_all()

    def _monitor_idle(self) -> None:
        while True:
            with self._condition:
                if self._closed:
                    return
                now = time.monotonic()
                expired = [
                    worker for worker in self._idle
                    if now - worker.last_used >= self.config.idle_seconds or not worker.alive
                ]
                for worker in expired:
                    self._retire_locked(worker)
                self._condition.wait(timeout=1.0)

    @staticmethod
    def _drain_worker_output(process: subprocess.Popen, kind: str) -> None:
        if process.stdout is None:
            return
        try:
            for line in process.stdout:
                text = line.decode("utf-8", errors="replace").rstrip()
                if text:
                    LOG.info("worker[%s] %s", kind, text)
        finally:
            try:
                process.stdout.close()
            except OSError:
                pass

    def close(self) -> None:
        with self._condition:
            self._closed = True
            workers = list(self._workers)
            self._workers.clear()
            self._idle.clear()
            self._condition.notify_all()
        for worker in workers:
            worker.close()


class SharedWorkerHandle:
    """A single heavy worker process shared by concurrent calls.

    Each call uses its own Unix-socket connection; the worker runtime handles
    them in threads. The model/heavy imports are therefore loaded once.
    """

    def __init__(
        self,
        *,
        kind: str,
        process: subprocess.Popen,
        socket_path: Path,
        temp_dir: Path,
        max_jobs: int,
        max_payload_bytes: int,
    ):
        self.kind = kind
        self.process = process
        self.socket_path = socket_path
        self.temp_dir = temp_dir
        self.max_jobs = max_jobs
        self.max_payload_bytes = max_payload_bytes
        self.jobs = 0
        self.last_used = time.monotonic()
        self._job_lock = threading.Lock()
        self._closed = False

    @property
    def alive(self) -> bool:
        return not self._closed and self.process.poll() is None

    def call(
        self,
        method: int,
        payload: bytes,
        *,
        cancel_check: Callable[[], bool] | None,
        timeout: float,
    ) -> bytes:
        connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.settimeout(0.5)
        try:
            try:
                connection.connect(str(self.socket_path))
                write_frame(connection, method, payload)
                status, response = read_frame(
                    connection,
                    max_payload_bytes=self.max_payload_bytes,
                    cancel_check=cancel_check,
                    deadline=time.monotonic() + timeout,
                    clock=time.monotonic,
                )
            except WorkerCancelled as exc:
                # Closing this connection is the per-call cancel signal; the
                # shared worker survives and other calls continue.
                raise WorkerRPCError("CANCELLED", "parser request was cancelled") from exc
            except TimeoutError as exc:
                raise WorkerUnavailable("worker response timed out") from exc
            except (OSError, WorkerProtocolError) as exc:
                raise WorkerUnavailable("worker connection failed") from exc
        finally:
            try:
                connection.close()
            except OSError:
                pass
        if status == STATUS_ERROR:
            code, message = decode_error(response)
            raise WorkerRPCError(code, message)
        with self._job_lock:
            self.jobs += 1
            self.last_used = time.monotonic()
        return response

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            if self.process.poll() is None:
                os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
                try:
                    self.process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(os.getpgid(self.process.pid), signal.SIGKILL)
                    try:
                        self.process.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        pass
        except (ProcessLookupError, PermissionError):
            pass
        try:
            shutil.rmtree(self.temp_dir, ignore_errors=True)
        except OSError:
            pass


class SharedWorkerPool:
    """One on-demand worker process with an internal thread pool.

    The server stays lightweight while idle. The worker is started on the
    first call, accepts up to ``concurrency`` simultaneous connections, and
    exits after the idle window; the next call starts a new one.
    """

    def __init__(self, config: WorkerPoolConfig):
        if config.concurrency <= 0:
            raise ValueError("concurrency must be positive")
        if config.idle_seconds <= 0 or config.queue_timeout < 0 or config.start_timeout <= 0:
            raise ValueError("worker pool timing values are invalid")
        self.config = config
        self._condition = threading.Condition()
        self._worker: SharedWorkerHandle | None = None
        self._active = 0
        self._starting = False
        self._closed = False
        self._monitor = threading.Thread(
            target=self._monitor_idle,
            name=f"parser-{config.kind}-shared-monitor",
            daemon=True,
        )
        self._monitor.start()

    @property
    def worker_count(self) -> int:
        with self._condition:
            return 0 if self._worker is None else 1

    def call(
        self,
        method: int,
        payload: bytes,
        *,
        cancel_check: Callable[[], bool] | None = None,
        timeout: float,
    ) -> bytes:
        worker = self._acquire(cancel_check)
        try:
            return worker.call(
                method,
                payload,
                cancel_check=cancel_check,
                timeout=timeout,
            )
        except WorkerUnavailable:
            with self._condition:
                if self._worker is worker and not worker.alive:
                    self._retire_locked()
            raise
        finally:
            with self._condition:
                self._active = max(0, self._active - 1)
                if self._worker is worker and worker.alive:
                    worker.last_used = time.monotonic()
                self._condition.notify_all()

    def _acquire(self, cancel_check: Callable[[], bool] | None = None) -> SharedWorkerHandle:
        deadline = time.monotonic() + self.config.queue_timeout
        while True:
            spawn = False
            with self._condition:
                if self._closed:
                    raise WorkerUnavailable("worker pool is closed")
                if cancel_check is not None and cancel_check():
                    raise WorkerRPCError("CANCELLED", "parser request was cancelled")
                if self._worker is not None and not self._worker.alive:
                    self._retire_locked()
                if self._worker is not None:
                    if self._active < self.config.concurrency:
                        self._active += 1
                        return self._worker
                elif not self._starting:
                    self._starting = True
                    spawn = True
                if not spawn:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise WorkerQueueFull("parser worker pool is saturated")
                    self._condition.wait(min(remaining, 0.5))
            if not spawn:
                continue
            try:
                worker = self._spawn()
            except Exception:
                with self._condition:
                    self._starting = False
                    self._condition.notify_all()
                raise
            with self._condition:
                self._starting = False
                if self._closed:
                    worker.close()
                else:
                    self._worker = worker
                self._condition.notify_all()

    def _spawn(self) -> SharedWorkerHandle:
        temp_dir = Path(tempfile.mkdtemp(prefix=f"papers-parser-{self.config.kind}-"))
        socket_path = temp_dir / "worker.sock"
        encoded = base64.b64encode(
            json.dumps(self.config.worker_config, separators=(",", ":")).encode("utf-8")
        ).decode("ascii")
        command = [
            sys.executable,
            "-m",
            "papers_parser.worker",
            "--kind",
            self.config.kind,
            "--socket",
            str(socket_path),
            "--config",
            encoded,
            "--idle-seconds",
            str(max(self.config.idle_seconds * 2, 5)),
            "--max-jobs",
            str(self.config.max_jobs),
            "--concurrency",
            str(self.config.concurrency),
        ]
        env = os.environ.copy()
        env.setdefault("PYTHONUNBUFFERED", "1")
        process = subprocess.Popen(
            command,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            close_fds=True,
            start_new_session=True,
            env=env,
        )
        threading.Thread(
            target=WorkerPool._drain_worker_output,
            args=(process, self.config.kind),
            name=f"parser-{self.config.kind}-worker-log",
            daemon=True,
        ).start()
        try:
            deadline = time.monotonic() + self.config.start_timeout
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise WorkerUnavailable("parser worker exited during startup")
                probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                probe.settimeout(0.2)
                try:
                    probe.connect(str(socket_path))
                    probe.close()
                    break
                except OSError:
                    probe.close()
                    time.sleep(0.05)
            else:
                raise WorkerUnavailable("parser worker startup timed out")
        except Exception:
            try:
                if process.poll() is None:
                    os.killpg(os.getpgid(process.pid), signal.SIGTERM)
                    process.wait(timeout=5)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(os.getpgid(process.pid), signal.SIGKILL)
                except ProcessLookupError:
                    pass
            shutil.rmtree(temp_dir, ignore_errors=True)
            raise
        return SharedWorkerHandle(
            kind=self.config.kind,
            process=process,
            socket_path=socket_path,
            temp_dir=temp_dir,
            max_jobs=self.config.max_jobs,
            max_payload_bytes=self.config.max_payload_bytes,
        )

    def _retire_locked(self) -> None:
        worker = self._worker
        self._worker = None
        if worker is not None:
            worker.close()
        self._condition.notify_all()

    def _monitor_idle(self) -> None:
        while True:
            with self._condition:
                if self._closed:
                    return
                worker = self._worker
                if worker is not None:
                    if not worker.alive:
                        self._retire_locked()
                    elif (
                        self._active == 0
                        and time.monotonic() - worker.last_used >= self.config.idle_seconds
                    ):
                        self._retire_locked()
                self._condition.wait(timeout=1.0)

    def close(self) -> None:
        with self._condition:
            self._closed = True
            self._retire_locked()
            self._condition.notify_all()
