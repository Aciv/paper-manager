"""Real child-process tests; the child has no PDF/model imports."""
from __future__ import annotations

import concurrent.futures
import json
import os
from pathlib import Path
import socket
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

from papers_parser import worker_transport as transport
from papers_parser.worker_pool import SharedWorkerPool, WorkerPoolConfig, WorkerRPCError, WorkerUnavailable
from papers_parser.worker_protocol import METHOD_EXTRACT


def child():
    from papers_parser.worker import _parse_args
    from papers_parser.worker_runtime import serve_shared_worker
    transport.await_parent()
    args = _parse_args()

    def handler(payload, context):
        if payload == b"crash":
            os._exit(17)
        started = time.monotonic()
        seconds = float(payload)
        while time.monotonic() - started < seconds:
            if not context.is_active():
                context.abort("CANCELLED", "cancelled")
            time.sleep(0.005)
        return json.dumps({"pid": os.getpid(), "start": started, "end": time.monotonic()}).encode()

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

    def pool(self, concurrency=4, idle=1):
        pool = SharedWorkerPool(WorkerPoolConfig(
            kind="extract", max_workers=1, idle_seconds=idle, max_jobs=100,
            queue_timeout=15, start_timeout=20, max_payload_bytes=4096,
            worker_config={}, concurrency=concurrency))
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


if __name__ == "__main__":
    if "--kind" in sys.argv:
        child()
    else:
        unittest.main()
