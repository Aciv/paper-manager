"""Real child-process tests; the child has no PDF/model imports."""
from __future__ import annotations

import concurrent.futures
import base64
import json
import io
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock
from types import SimpleNamespace

from papers_parser import worker_transport as transport
from papers_parser.worker_pool import SharedWorkerPool, WorkerPoolConfig, WorkerRPCError, WorkerUnavailable
from papers_parser.worker_protocol import METHOD_EXTRACT
from papers_parser.worker_protocol import WorkerProtocolError, encode_frame, read_frame, write_frame


def child():
    from papers_parser.worker import _parse_args
    from papers_parser.worker_runtime import serve_shared_worker
    transport.await_parent()
    args = _parse_args()
    config = json.loads(base64.b64decode(args.config))
    trace_lock = threading.Lock()

    def handler(payload, context):
        if payload == b"crash":
            os._exit(17)
        if payload == b"spawn-helper":
            helper = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
            return json.dumps({"worker": os.getpid(), "helper": helper.pid}).encode()
        started = time.monotonic()
        uninterruptible = payload.startswith(b"native:")
        seconds = float(payload.removeprefix(b"native:"))
        while time.monotonic() - started < seconds:
            if not uninterruptible and not context.is_active():
                context.abort("CANCELLED", "cancelled")
            time.sleep(0.005)
        result = {"pid": os.getpid(), "start": started, "end": time.monotonic()}
        if config.get("trace_path"):
            with trace_lock, open(config["trace_path"], "a", encoding="utf-8") as trace:
                trace.write(json.dumps(result) + "\n")
        return json.dumps(result).encode()

    serve_shared_worker(socket_path=args.socket, expected_method=METHOD_EXTRACT,
                        handler=handler, idle_seconds=args.idle_seconds,
                        max_jobs=args.max_jobs, max_payload_bytes=4096, concurrency=args.concurrency)


class SharedLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pm worker 空格 ")
        self.addCleanup(self.temporary.cleanup)
        original = transport.start_process

        def start(command, **kwargs):
            command = list(command)
            command[command.index("papers_parser.worker")] = "papers_parser.test_worker_transport"
            return original(command, **kwargs)

        self.patch = mock.patch("papers_parser.worker_pool.start_process", side_effect=start)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def pool(self, concurrency=4, idle=1, worker_config=None):
        pool = SharedWorkerPool(WorkerPoolConfig(
            kind="extract", max_workers=1, idle_seconds=idle, max_jobs=100,
            queue_timeout=15, start_timeout=20, max_payload_bytes=4096,
            worker_config=worker_config or {}, concurrency=concurrency))
        self.addCleanup(pool.close)
        return pool

    def call(self, pool, seconds=b"0.08", **kwargs):
        return json.loads(pool.call(METHOD_EXTRACT, seconds, timeout=5, **kwargs))

    def test_twenty_jobs_overlap_with_one_worker_and_budget_four(self):
        pool = self.pool()
        with concurrent.futures.ThreadPoolExecutor(max_workers=20) as clients:
            results = list(clients.map(lambda _: self.call(pool), range(20)))
        self.assertEqual(len({result["pid"] for result in results}), 1)
        events = sorted([(result["start"], 1) for result in results] +
                        [(result["end"], -1) for result in results])
        active = peak = 0
        for _, change in events:
            active += change
            peak = max(peak, active)
        self.assertGreater(peak, 1)
        self.assertLessEqual(peak, 4)
        worker = pool._worker
        pool.close()
        pool.close()
        self.assertIsNotNone(worker.process.poll())
        self.assertFalse(worker.temp_dir.exists())

    def test_one_cancel_does_not_kill_other_request(self):
        pool = self.pool()
        initial = self.call(pool)
        cancel = threading.Event()
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as clients:
            pending = clients.submit(self.call, pool, b"3", cancel_check=cancel.is_set)
            survivor = clients.submit(self.call, pool, b"0.8")
            time.sleep(0.15)
            cancel.set()
            with self.assertRaises(WorkerRPCError) as raised:
                pending.result(timeout=5)
            self.assertEqual(raised.exception.code, "CANCELLED")
            self.assertEqual(survivor.result(timeout=5)["pid"], initial["pid"])
        self.assertEqual(self.call(pool)["pid"], initial["pid"])

    def test_crash_restarts_and_idle_reclaims(self):
        pool = self.pool(concurrency=1)
        first = self.call(pool)
        with self.assertRaises(WorkerUnavailable):
            self.call(pool, b"crash")
        second = self.call(pool)
        self.assertNotEqual(first["pid"], second["pid"])
        worker = pool._worker
        deadline = time.monotonic() + 8
        while pool.worker_count and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertEqual(pool.worker_count, 0)
        self.assertIsNotNone(worker.process.poll())

    def test_timeout_is_per_connection(self):
        pool = self.pool()
        initial = self.call(pool)
        with self.assertRaises(WorkerUnavailable):
            pool.call(METHOD_EXTRACT, b"3", timeout=0.1)
        self.assertEqual(self.call(pool)["pid"], initial["pid"])

    def test_native_calls_keep_physical_budget_after_clients_time_out(self):
        trace = Path(self.temporary.name) / "spans.jsonl"
        pool = self.pool(idle=5, worker_config={"trace_path": str(trace)})
        self.call(pool)  # initialize worker before the concurrent timeout batch
        trace.write_text("", encoding="utf-8")

        def timeout():
            with self.assertRaises(WorkerUnavailable):
                pool.call(METHOD_EXTRACT, b"native:2", timeout=.1)

        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as clients:
            list(clients.map(lambda _: timeout(), range(4)))
            results = list(clients.map(lambda _: self.call(pool), range(4)))
        spans = [json.loads(line) for line in trace.read_text(encoding="utf-8").splitlines()]
        self.assertEqual(len(spans), 8)
        self.assertEqual(len({span["pid"] for span in spans}), 1)
        events = sorted([(span["start"], 1) for span in spans] + [(span["end"], -1) for span in spans])
        active = peak = 0
        for _, change in events:
            active += change
            peak = max(peak, active)
        self.assertEqual(peak, 4)

    def test_parent_death_cleans_worker_and_native_helper(self):
        import psutil  # validation dependency, never imported by the server
        endpoint = Path(self.temporary.name) / "worker.sock"
        script = '''
import json, os, secrets, sys, time
from pathlib import Path
from papers_parser.worker_transport import start_process, connect, TOKEN_ENV
from papers_parser.worker_protocol import read_frame, write_frame, METHOD_EXTRACT
token = secrets.token_hex(32)
process = start_process([sys.executable, '-m', 'papers_parser.test_worker_transport',
    '--kind', 'extract', '--shared', '--socket', sys.argv[1], '--config', 'e30=',
    '--idle-seconds', '60', '--max-jobs', '100', '--concurrency', '4'],
    env={**os.environ, TOKEN_ENV: token}, cwd=sys.argv[2])
deadline = time.monotonic() + 20
while True:
    try:
        connection = connect(Path(sys.argv[1]), token)
        break
    except OSError:
        if time.monotonic() > deadline:
            raise
        time.sleep(.05)
write_frame(connection, METHOD_EXTRACT, b'spawn-helper')
_, result = read_frame(connection, max_payload_bytes=4096)
connection.close()
print(result.decode(), flush=True)
time.sleep(60)
'''
        root = str(Path(__file__).resolve().parent.parent)
        parent = subprocess.Popen([sys.executable, "-c", script, str(endpoint), root],
                                  cwd=root, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  text=True, env={**os.environ, "PYTHONPATH": root})
        owned = []
        try:
            # communicate timeout also prevents hanging on a broken handshake.
            def read_identity():
                return parent.stdout.readline()
            with concurrent.futures.ThreadPoolExecutor() as clients:
                pending = clients.submit(read_identity)
                try:
                    line = pending.result(timeout=25)
                except Exception:
                    parent.kill()
                    raise
            identity = json.loads(line)
            owned = [psutil.Process(pid) for pid in identity.values()]
            parent.kill()
            parent.wait(timeout=5)
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                alive = [p for p in owned if p.is_running() and p.status() != psutil.STATUS_ZOMBIE]
                if not alive:
                    break
                time.sleep(.05)
            self.assertFalse(alive, f"orphan worker/helper: {[p.pid for p in alive]}")
        finally:
            if parent.poll() is None:
                parent.kill()
            parent.communicate(timeout=5)
            for process in owned:
                try:
                    if process.is_running() and process.status() != psutil.STATUS_ZOMBIE:
                        process.kill()
                except psutil.NoSuchProcess:
                    pass


class TCPAuthenticationTest(unittest.TestCase):
    def test_windows_parent_handshake_does_not_start_a_stdin_reader(self):
        stream = io.BytesIO(b"\x01")
        with mock.patch.object(transport.os, "name", "nt"), \
                mock.patch.object(transport.sys, "stdin", SimpleNamespace(buffer=stream)), \
                mock.patch.dict(os.environ, {transport.PARENT_ENV: "1"}), \
                mock.patch.object(transport.threading, "Thread") as thread:
            transport.await_parent()
        self.assertEqual(stream.tell(), 1)
        thread.assert_not_called()

    def test_valid_and_invalid_keys_over_real_loopback(self):
        with tempfile.TemporaryDirectory() as temporary, \
                mock.patch.object(transport, "_TCP", True), \
                mock.patch.dict(os.environ, {transport.TOKEN_ENV: "a" * 64}):
            path = Path(temporary) / "endpoint.json"
            listener = transport.listen(path, 4)
            self.addCleanup(listener.close)
            listener.settimeout(3)
            results = []

            def accept():
                for _ in range(2):
                    connection, _ = listener.accept()
                    with connection:
                        results.append(transport.authenticate(connection))

            with concurrent.futures.ThreadPoolExecutor() as clients:
                future = clients.submit(accept)
                with self.assertRaises(OSError):
                    transport.connect(path, "b" * 64)
                transport.connect(path, "a" * 64).close()
                future.result(timeout=5)
            self.assertEqual(results, [False, True])
            self.assertNotIn("a" * 64, path.read_text())


class FrameBoundaryTest(unittest.TestCase):
    def test_write_keeps_progress_after_socket_poll_timeout(self):
        class SlowSocket:
            def __init__(self):
                self.sent = bytearray()
                self.calls = 0

            def send(self, data):
                self.calls += 1
                if self.calls == 2:
                    raise socket.timeout()
                size = min(len(data), 3)
                self.sent.extend(data[:size])
                return size

        connection = SlowSocket()
        write_frame(connection, METHOD_EXTRACT, b"bounded frame", deadline=time.monotonic() + 1)
        self.assertEqual(connection.sent, encode_frame(METHOD_EXTRACT, b"bounded frame"))

    def test_frame_limit_rejects_header_before_reading_payload(self):
        class HeaderSocket:
            def recv(self, size):
                self.assert_size = size
                return encode_frame(METHOD_EXTRACT, b"oversized")[:5]

        with self.assertRaises(WorkerProtocolError):
            read_frame(HeaderSocket(), max_payload_bytes=2)

    def test_deadline_applies_even_when_peer_keeps_sending(self):
        class BusySocket:
            def recv(self, size):
                self.calls += 1
                return b"x"

            calls = 0

        connection = BusySocket()
        with self.assertRaises(TimeoutError):
            read_frame(connection, max_payload_bytes=1024,
                       deadline=0, clock=lambda: 1)
        self.assertEqual(connection.calls, 0)


if __name__ == "__main__":
    if "--kind" in sys.argv:
        child()
    else:
        unittest.main()
