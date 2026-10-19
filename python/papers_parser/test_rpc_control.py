"""Server-side cancellation, deadline, concurrency, and shutdown tests (P1-9)."""
from __future__ import annotations

import signal
import tempfile
import threading
import time
import unittest
import uuid
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import grpc
import pymupdf

import document_parser_pb2
import papers_parser.server as server_module
from papers_parser.pdf2md_service import (
    DocumentParser,
    extract_layout_lines_from_pdf,
    extract_layout_lines_from_storage,
)
from papers_parser.rpc_control import check_rpc


class _Context:
    def __init__(self, active_calls: int = 100) -> None:
        self.active_calls = active_calls
        self.active_checks = 0
        self.aborted: list[grpc.StatusCode] = []

    def is_active(self) -> bool:
        self.active_checks += 1
        return self.active_checks <= self.active_calls

    def time_remaining(self):
        return None

    def abort(self, code, message):
        self.aborted.append(code)
        raise grpc.RpcError()


class RpcControlTest(unittest.TestCase):
    def test_check_rpc_allows_active_rpc(self) -> None:
        check_rpc(_Context(), time.monotonic(), 60)

    def test_check_rpc_aborts_cancelled_rpc(self) -> None:
        context = _Context(active_calls=0)
        with self.assertRaises(grpc.RpcError):
            check_rpc(context, time.monotonic(), 60)
        self.assertEqual(context.aborted, [grpc.StatusCode.CANCELLED])

    def test_check_rpc_aborts_when_client_deadline_expired(self) -> None:
        context = _Context()
        context.time_remaining = lambda: 0
        with self.assertRaises(grpc.RpcError):
            check_rpc(context, time.monotonic(), 60)
        self.assertEqual(context.aborted, [grpc.StatusCode.DEADLINE_EXCEEDED])

    def test_check_rpc_aborts_when_server_cap_exceeded(self) -> None:
        context = _Context()
        with self.assertRaises(grpc.RpcError):
            check_rpc(context, time.monotonic() - 10, 1)
        self.assertEqual(context.aborted, [grpc.StatusCode.DEADLINE_EXCEEDED])

    def test_cancellation_during_page_extraction_stops_before_blocks(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            parser = DocumentParser(Path(directory), 1 << 20, 72)
            document = pymupdf.open()
            document.new_page()
            content = document.tobytes()
            document.close()
            request = document_parser_pb2.RecognitionRequest(
                document_id=str(uuid.uuid4()),
                filename="cancel.pdf",
                content_type="application/pdf",
                content=content,
            )
            context = _Context(active_calls=3)
            with mock.patch(
                "papers_parser.pdf2md_service.pymupdf4llm.to_markdown",
                return_value=[{"text": "cancel", "page_boxes": []}],
            ):
                with self.assertRaises(grpc.RpcError):
                    parser.Recognize(request, context)
            self.assertEqual(context.aborted, [grpc.StatusCode.CANCELLED])

    def test_layout_extraction_polls_checkpoint_per_page(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "layout.pdf"
            document = pymupdf.open()
            document.new_page()
            document.new_page()
            document.save(str(path))
            document.close()
            checks = 0

            def checkpoint() -> None:
                nonlocal checks
                checks += 1

            extract_layout_lines_from_pdf(path, (), 100, checkpoint)
            self.assertEqual(checks, 2)
            extract_layout_lines_from_storage(
                Path(directory), "layout.pdf", (), 100, checkpoint
            )
            self.assertEqual(checks, 4)


class ServerBootstrapTest(unittest.TestCase):
    def test_create_server_bounds_workers_and_concurrent_rpcs(self) -> None:
        args = SimpleNamespace(
            max_workers=4,
            max_concurrent_rpcs=3,
            grpc_max_message_size=12345,
        )
        with mock.patch.object(server_module.futures, "ThreadPoolExecutor") as executor:
            with mock.patch.object(server_module.grpc, "server") as grpc_server:
                server_module._create_server(args)
        executor.assert_called_once_with(max_workers=4)
        kwargs = grpc_server.call_args.kwargs
        self.assertEqual(kwargs["maximum_concurrent_rpcs"], 3)
        self.assertEqual(
            kwargs["options"],
            [
                ("grpc.max_receive_message_length", 12345),
                ("grpc.max_send_message_length", 12345),
            ],
        )

    def test_sigterm_requests_graceful_shutdown(self) -> None:
        previous_term = signal.getsignal(signal.SIGTERM)
        previous_int = signal.getsignal(signal.SIGINT)
        try:
            stop_requested = server_module._install_shutdown_handlers()
            handler = signal.getsignal(signal.SIGTERM)
            handler(signal.SIGTERM, None)
            self.assertTrue(stop_requested.is_set())
        finally:
            signal.signal(signal.SIGTERM, previous_term)
            signal.signal(signal.SIGINT, previous_int)

    def test_max_concurrent_rpcs_rejects_excess_requests(self) -> None:
        args = SimpleNamespace(
            max_workers=2,
            max_concurrent_rpcs=1,
            grpc_max_message_size=1 << 20,
        )
        server = server_module._create_server(args)
        server.add_generic_rpc_handlers(
            (
                grpc.method_handlers_generic_handler(
                    "test.Svc",
                    {
                        "Slow": grpc.unary_unary_rpc_method_handler(
                            lambda _request, _context: time.sleep(0.3) or b"ok"
                        )
                    },
                ),
            )
        )
        port = server.add_insecure_port("127.0.0.1:0")
        server.start()
        channel = grpc.insecure_channel(f"127.0.0.1:{port}")
        call = channel.unary_unary("/test.Svc/Slow")
        first_result: list[bytes] = []
        first = threading.Thread(target=lambda: first_result.append(call(b"x", timeout=2)))
        first.start()
        time.sleep(0.1)
        try:
            with self.assertRaises(grpc.RpcError) as raised:
                call(b"y", timeout=1)
            self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)
        finally:
            first.join(3)
            server.stop(0).wait(2)
            channel.close()
        self.assertEqual(first_result, [b"ok"])

    def test_graceful_stop_cancels_inflight_after_grace(self) -> None:
        server = grpc.server(
            server_module.futures.ThreadPoolExecutor(max_workers=1)
        )
        started = threading.Event()

        def slow(_request, _context):
            started.set()
            time.sleep(1)
            return b"ok"

        server.add_generic_rpc_handlers(
            (
                grpc.method_handlers_generic_handler(
                    "test.Svc",
                    {"Slow": grpc.unary_unary_rpc_method_handler(slow)},
                ),
            )
        )
        port = server.add_insecure_port("127.0.0.1:0")
        server.start()
        channel = grpc.insecure_channel(f"127.0.0.1:{port}")
        call = channel.unary_unary("/test.Svc/Slow")
        result: list[grpc.StatusCode] = []
        worker = threading.Thread(
            target=lambda: _record_call_error(call, result)
        )
        worker.start()
        try:
            self.assertTrue(started.wait(1))
            started_stop = time.monotonic()
            stopped = server.stop(grace=0.1)
            self.assertTrue(stopped.wait(3))
            worker.join(3)
            # gRPC reports a force-closed in-flight RPC as CANCELLED or
            # UNAVAILABLE depending on the transport race; both are terminal
            # and the important property is that it does not hang.
            self.assertTrue(result, "in-flight RPC did not finish")
            self.assertIn(
                result[0],
                {grpc.StatusCode.CANCELLED, grpc.StatusCode.UNAVAILABLE},
            )
            self.assertLess(time.monotonic() - started_stop, 2.5)
        finally:
            server.stop(0).wait(2)
            channel.close()


def _record_call_error(call, result: list[grpc.StatusCode]) -> None:
    try:
        call(b"x", timeout=5)
    except grpc.RpcError as exc:
        result.append(exc.code())


if __name__ == "__main__":
    unittest.main()
