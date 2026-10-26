"""Worker lifecycle: shared/process pools, idle retirement, transport framing/authentication, server imports.

Consolidated parser test module; see the per-test docstrings for the exact contract.
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from pathlib import Path
import pymupdf
import document_parser_pb2
from papers_parser.worker_pool import SharedWorkerPool, WorkerPool, WorkerPoolConfig, WorkerRPCError
from papers_parser.worker_protocol import METHOD_EXTRACT, METHOD_RECOGNIZE, METHOD_RENDER
import concurrent.futures
import base64
import json
import socket
import threading
from unittest import mock
from papers_parser import worker_transport as transport
from papers_parser.worker_pool import SharedWorkerPool, WorkerPool, WorkerPoolConfig, WorkerRPCError, WorkerUnavailable
from papers_parser.worker_protocol import METHOD_EXTRACT
from papers_parser.worker_protocol import WorkerProtocolError, encode_frame, read_frame, write_frame

# ---------------------------------------------------------------------------
# Consolidated from test_server_worker.py.
# ---------------------------------------------------------------------------

def make_pdf() -> bytes:
    document = pymupdf.open()
    page = document.new_page()
    page.insert_text((72, 72), "A tiny parser worker test paper.")
    data = document.tobytes()
    document.close()
    return data


def worker_config(upload_dir: Path) -> dict:
    return {
        "upload_dir": str(upload_dir),
        "max_file_size": 8 << 20,
        "image_dpi": 72,
        "max_pages": 10,
        "max_page_edge_points": 5000.0,
        "max_crop_pixels": 1_000_000,
        "max_crops_per_document": 10,
        "max_crop_bytes": 1 << 20,
        "max_recognition_markdown_bytes": 1 << 20,
        "max_extraction_markdown_bytes": 1 << 20,
        "paper_extraction_engine": "rules",
        "paper_crf_model_dir": str(upload_dir),
        "max_extraction_blocks": 100,
        "max_extraction_layout_lines": 100,
        "grpc_max_message_size": 8 << 20,
        "grpc_message_overhead": 1 << 20,
        "rpc_max_seconds": 30,
        "render_max_dpi": 300,
        "render_max_pixels": 1_000_000,
        "render_max_output_bytes": 8 << 20,
    }


class WorkerPoolTest(unittest.TestCase):
    def setUp(self) -> None:
        self.directory = tempfile.TemporaryDirectory()
        self.upload_dir = Path(self.directory.name)
        self.pools: list[WorkerPool] = []

    def tearDown(self) -> None:
        for pool in self.pools:
            pool.close()
        self.directory.cleanup()

    def pool(self, kind: str, *, idle_seconds: float = 1.0) -> WorkerPool:
        pool = WorkerPool(
            WorkerPoolConfig(
                kind=kind,
                max_workers=1,
                idle_seconds=idle_seconds,
                max_jobs=10,
                queue_timeout=15,
                start_timeout=60,
                max_payload_bytes=8 << 20,
                worker_config=worker_config(self.upload_dir),
            )
        )
        self.pools.append(pool)
        return pool

    @staticmethod
    def wait_for_no_workers(pool: WorkerPool, timeout: float = 8.0) -> bool:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if pool.worker_count == 0:
                return True
            time.sleep(0.2)
        return False

    def test_recognize_worker_round_trip_and_idle_shutdown(self) -> None:
        """Verify that a recognition worker returns a parsed result and exits after going idle."""
        pool = self.pool("recognize")
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()),
            filename="worker.pdf",
            content_type="application/pdf",
            content=make_pdf(),
        )
        response = document_parser_pb2.RecognitionResult()
        response.ParseFromString(
            pool.call(METHOD_RECOGNIZE, request.SerializeToString(), timeout=60)
        )
        self.assertEqual(response.page_count, 1)
        self.assertIn("tiny parser worker test paper", response.markdown.decode("utf-8").lower())
        self.assertTrue(self.wait_for_no_workers(pool), "idle recognition worker did not exit")

    def test_render_worker_round_trip_and_idle_shutdown(self) -> None:
        """Verify that a render worker returns PNG data and exits after going idle."""
        source = self.upload_dir / "page.pdf"
        source.write_bytes(make_pdf())
        pool = self.pool("render")
        request = document_parser_pb2.PdfRenderRequest(
            document_id=str(uuid.uuid4()),
            source_storage_key="page.pdf",
            page_number=1,
            full_page=True,
            dpi=72,
        )
        response = document_parser_pb2.PdfRenderResult()
        response.ParseFromString(
            pool.call(METHOD_RENDER, request.SerializeToString(), timeout=60)
        )
        self.assertTrue(response.png.startswith(b"\x89PNG\r\n\x1a\n"))
        self.assertGreater(response.width, 0)
        self.assertGreater(response.page_width, 0)
        self.assertTrue(self.wait_for_no_workers(pool), "idle render worker did not exit")

    def test_extract_worker_round_trip_and_idle_shutdown(self) -> None:
        """Verify that an extraction worker returns a result and exits after going idle."""
        pool = self.pool("extract")
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid.uuid4()),
            markdown=b"# A Tiny Paper\n\nThis abstract describes a parser worker test.",
            blocks=[],
        )
        response = document_parser_pb2.PaperExtractionResult()
        response.ParseFromString(
            pool.call(METHOD_EXTRACT, request.SerializeToString(), timeout=60)
        )
        self.assertTrue(response.extractor_name)
        self.assertTrue(self.wait_for_no_workers(pool), "idle extraction worker did not exit")

    def test_shared_recognition_reuses_worker_for_key_and_bytes_inputs(self):
        """Verify that a shared recognition worker serves key and byte requests from the same process."""
        source = self.upload_dir / "source.pdf"
        source.write_bytes(make_pdf())
        pool = SharedWorkerPool(WorkerPoolConfig(
            kind="recognize", max_workers=1, concurrency=4, idle_seconds=5,
            max_jobs=20, queue_timeout=15, start_timeout=60,
            max_payload_bytes=8 << 20, worker_config=worker_config(self.upload_dir)))
        self.pools.append(pool)
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()), filename="source.pdf", content_type="application/pdf",
            source_storage_key="source.pdf", source_size_bytes=source.stat().st_size)
        result = document_parser_pb2.RecognitionResult.FromString(pool.call(METHOD_RECOGNIZE, request.SerializeToString(), timeout=30))
        self.assertEqual(result.page_count, 1)
        pid = pool._worker.process.pid
        request.source_storage_key = "missing.pdf"
        with self.assertRaises(WorkerRPCError) as raised:
            pool.call(METHOD_RECOGNIZE, request.SerializeToString(), timeout=30)
        self.assertEqual(raised.exception.code, "NOT_FOUND")
        request.content = source.read_bytes()  # protobuf oneof replaces the key
        result = document_parser_pb2.RecognitionResult.FromString(pool.call(METHOD_RECOGNIZE, request.SerializeToString(), timeout=30))
        self.assertEqual(result.page_count, 1)
        self.assertEqual(pool._worker.process.pid, pid)


class ServerImportTest(unittest.TestCase):
    def test_server_import_does_not_load_heavy_parser_dependencies(self) -> None:
        """Verify that importing the server module does not load heavy parser dependencies."""
        parser_dir = Path(__file__).resolve().parents[2]
        env = os.environ.copy()
        env["PYTHONPATH"] = str(parser_dir) + os.pathsep + env.get("PYTHONPATH", "")
        script = (
            "import sys; import papers_parser.server; "
            "heavy=[name for name in ('pymupdf','pymupdf4llm','pymupdf_layout','onnxruntime','pycrfsuite','numpy') if name in sys.modules]; "
            "print(','.join(heavy))"
        )
        completed = subprocess.run(
            [sys.executable, "-c", script],
            cwd=parser_dir,
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stdout.strip(), "", completed.stdout)

    def test_extract_worker_dependency_import_does_not_load_recognition_stack(self) -> None:
        """Verify that building a rules extraction engine does not import the recognition stack."""
        parser_dir = Path(__file__).resolve().parents[2]
        env = os.environ.copy()
        env["PYTHONPATH"] = str(parser_dir) + os.pathsep + env.get("PYTHONPATH", "")
        script = (
            "import sys, pathlib; "
            "from papers_parser.engine_factory import build_extraction_engine; "
            "build_extraction_engine('rules', pathlib.Path('/tmp')); "
            "heavy=[name for name in ('pymupdf4llm','pymupdf_layout','onnxruntime','pycrfsuite','paper_crf') if name in sys.modules]; "
            "print(','.join(heavy))"
        )
        completed = subprocess.run(
            [sys.executable, "-c", script],
            cwd=parser_dir,
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stdout.strip(), "", completed.stdout)


# ---------------------------------------------------------------------------
# Consolidated from test_worker_transport.py.
# ---------------------------------------------------------------------------

def child():
    from papers_parser.worker import _parse_args
    from papers_parser.worker_runtime import serve_shared_worker
    transport.await_parent()
    args = _parse_args()
    config = json.loads(base64.b64decode(args.config))
    if config.get("tcp"):
        transport._TCP = True
    if config.get("startup_delay"):
        time.sleep(config["startup_delay"])
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
        # Exercise Windows's TCP handshake/queue path on every platform, too.
        tcp_patch = mock.patch.object(transport, "_TCP", True)
        tcp_patch.start()
        self.addCleanup(tcp_patch.stop)
        original = transport.start_process
        self.started = threading.Event()
        self.processes = []

        def start(command, **kwargs):
            command = list(command)
            command[command.index("papers_parser.worker")] = "papers_parser.test.test_workers"
            process = original(command, **kwargs)
            self.processes.append((process, Path(command[command.index("--socket") + 1]).parent))
            self.started.set()
            return process

        self.patch = mock.patch("papers_parser.worker_pool.start_process", side_effect=start)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def pool(self, concurrency=4, idle=1, worker_config=None):
        pool = SharedWorkerPool(WorkerPoolConfig(
            kind="extract", max_workers=1, idle_seconds=idle, max_jobs=100,
            queue_timeout=15, start_timeout=20, max_payload_bytes=4096,
            worker_config={"tcp": True, **(worker_config or {})}, concurrency=concurrency))
        self.addCleanup(pool.close)
        return pool

    def call(self, pool, seconds=b"0.08", **kwargs):
        return json.loads(pool.call(METHOD_EXTRACT, seconds, timeout=5, **kwargs))

    def test_twenty_jobs_overlap_with_one_worker_and_budget_four(self):
        """Verify that twenty concurrent jobs share one worker with overlap capped at four."""
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

    def test_long_private_path_uses_one_authenticated_shared_worker(self):
        """Verify that a long Unicode path still yields one authenticated shared worker over the TCP fallback."""
        root = Path(self.temporary.name) / ("目录" * 25)
        root.mkdir()
        original = tempfile.mkdtemp
        with mock.patch.object(transport, "_TCP", False), mock.patch(
            "papers_parser.worker_pool.tempfile.mkdtemp",
            side_effect=lambda **kwargs: original(dir=str(root), **kwargs),
        ):
            pool = self.pool(worker_config={"tcp": False})
            with concurrent.futures.ThreadPoolExecutor(max_workers=20) as clients:
                results = list(clients.map(lambda _: self.call(pool, b"0.02"), range(20)))
            self.assertEqual(len({result["pid"] for result in results}), 1)
            self.assertTrue(pool._worker.socket_path.is_file())
            pool.close()

    def test_one_cancel_does_not_kill_other_request(self):
        """Verify that cancelling one request leaves another and subsequent requests on the same worker."""
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
        """Verify that a crashed worker is replaced and then reclaimed after idling."""
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
        """Verify that a per-call timeout does not terminate the worker or affect later calls."""
        pool = self.pool()
        initial = self.call(pool)
        with self.assertRaises(WorkerUnavailable):
            pool.call(METHOD_EXTRACT, b"3", timeout=0.1)
        self.assertEqual(self.call(pool)["pid"], initial["pid"])

    def test_start_failure_removes_private_directory_and_returns_typed_error(self):
        """Verify that a worker start failure removes the private directory and raises WorkerUnavailable."""
        for pool_class in (WorkerPool, SharedWorkerPool):
            with self.subTest(pool_class=pool_class.__name__):
                directory = Path(self.temporary.name) / pool_class.__name__
                directory.mkdir()
                pool = pool_class(WorkerPoolConfig(
                    kind="extract", max_workers=1, idle_seconds=5, max_jobs=10,
                    queue_timeout=5, start_timeout=5, max_payload_bytes=4096,
                    worker_config={}, concurrency=1))
                self.addCleanup(pool.close)
                with mock.patch("papers_parser.worker_pool.tempfile.mkdtemp", return_value=str(directory)), \
                        mock.patch("papers_parser.worker_pool.start_process", side_effect=OSError("fixture spawn failure")):
                    with self.assertRaises(WorkerUnavailable):
                        pool.call(METHOD_EXTRACT, b"0", timeout=1)
                self.assertFalse(directory.exists())
                pool.close()

    def test_close_interrupts_child_startup_and_cleans_process(self):
        """Verify that closing during child startup aborts the call and cleans up the process and directory."""
        pool = self.pool(worker_config={"startup_delay": 60})
        with concurrent.futures.ThreadPoolExecutor() as clients:
            future = clients.submit(self.call, pool)
            self.assertTrue(self.started.wait(timeout=5))
            pool.close()
            with self.assertRaises(WorkerUnavailable):
                future.result(timeout=5)
        self.assertEqual(len(self.processes), 1)
        process, directory = self.processes[0]
        self.assertIsNotNone(process.poll())
        self.assertFalse(directory.exists())

    def test_native_calls_keep_physical_budget_after_clients_time_out(self):
        """Verify that timed-out native calls still hold physical slots until they finish, capping concurrency at four."""
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
        """Verify that killing the parent process also terminates its worker and native helper."""
        import psutil  # validation dependency, never imported by the server
        endpoint = Path(self.temporary.name) / "worker.sock"
        script = '''
import json, os, secrets, sys, time
from pathlib import Path
from papers_parser.worker_transport import start_process, connect, TOKEN_ENV
from papers_parser.worker_protocol import read_frame, write_frame, METHOD_EXTRACT
token = secrets.token_hex(32)
process = start_process([sys.executable, '-m', 'papers_parser.test.test_workers',
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
        root = str(Path(__file__).resolve().parents[2])
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

            def still_running():
                alive = []
                for process in owned:
                    try:
                        if process.is_running() and process.status() != psutil.STATUS_ZOMBIE:
                            alive.append(process)
                    except psutil.NoSuchProcess:
                        # Job Object cleanup can complete between the two
                        # observations. Disappearance is the expected result.
                        pass
                return alive

            while time.monotonic() < deadline:
                alive = still_running()
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
        """Verify that the Windows parent handshake reads a single byte without starting a stdin thread."""
        with mock.patch.object(transport.os, "name", "nt"), \
                mock.patch.object(transport.os, "read", return_value=b"\x01") as read, \
                mock.patch.dict(os.environ, {transport.PARENT_ENV: "1"}), \
                mock.patch.object(transport.threading, "Thread") as thread:
            transport.await_parent()
        read.assert_called_once_with(0, 1)
        thread.assert_not_called()

    def test_long_unicode_path_keeps_tcp_authentication_on_unix(self):
        """Verify that long Unix paths fall back to authenticated TCP without writing the token to disk."""
        with tempfile.TemporaryDirectory() as temporary, \
                mock.patch.object(transport, "_TCP", False), \
                mock.patch.dict(os.environ, {transport.TOKEN_ENV: "a" * 64}):
            root = Path(temporary) / ("目录" * 25)
            root.mkdir()
            path = root / "worker.sock"
            listener = transport.listen(path, 4)
            self.addCleanup(listener.close)
            listener.settimeout(3)
            self.assertEqual(listener.family, socket.AF_INET)
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

    def test_valid_and_invalid_keys_over_real_loopback(self):
        """Verify that loopback TCP authentication accepts the matching token and rejects a wrong one."""
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
        """Verify that a write retries after a socket poll timeout and preserves all sent bytes."""
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
        """Verify that an oversized frame header is rejected before its payload is read."""
        class HeaderSocket:
            def recv(self, size):
                self.assert_size = size
                return encode_frame(METHOD_EXTRACT, b"oversized")[:5]

        with self.assertRaises(WorkerProtocolError):
            read_frame(HeaderSocket(), max_payload_bytes=2)

    def test_deadline_applies_even_when_peer_keeps_sending(self):
        """Verify that a zero deadline fails without reading when a peer keeps sending data."""
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
