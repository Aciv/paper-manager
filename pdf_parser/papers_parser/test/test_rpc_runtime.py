"""RPC controls and runtime operations: deadlines/cancellation, engine fallback, logging, packaged bundles.

Consolidated parser test module; see the per-test docstrings for the exact contract.
"""

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
from papers_parser.pdf2md_service import DocumentParser
from papers_parser.layout_lines import (
    extract_layout_lines_from_pdf,
    extract_layout_lines_from_storage,
)
from papers_parser.rpc_control import check_rpc
import json
import logging
import os
from tempfile import TemporaryDirectory
from unittest.mock import patch
import logger
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine, LayoutLine
from papers_parser.server import PROJECT_ROOT, _build_extraction_engine, _resolve_project_path

# ---------------------------------------------------------------------------
# Consolidated from test_rpc_control.py.
# ---------------------------------------------------------------------------

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
    def test_windows_startup_does_not_load_glibc_allocator(self) -> None:
        """Verify that memory trimming does not load the glibc allocator on Windows."""
        with mock.patch.object(server_module.os, "name", "nt"), \
                mock.patch.object(server_module.ctypes, "CDLL") as load_library:
            server_module._start_memory_trim(60)
        load_library.assert_not_called()

    def test_check_rpc_allows_active_rpc(self) -> None:
        """Verify that an active RPC with time remaining passes the control check."""
        check_rpc(_Context(), time.monotonic(), 60)

    def test_check_rpc_aborts_cancelled_rpc(self) -> None:
        """Verify that an RPC whose context is no longer active is aborted with CANCELLED."""
        context = _Context(active_calls=0)
        with self.assertRaises(grpc.RpcError):
            check_rpc(context, time.monotonic(), 60)
        self.assertEqual(context.aborted, [grpc.StatusCode.CANCELLED])

    def test_check_rpc_aborts_when_client_deadline_expired(self) -> None:
        """Verify that an expired client deadline aborts the RPC with DEADLINE_EXCEEDED."""
        context = _Context()
        context.time_remaining = lambda: 0
        with self.assertRaises(grpc.RpcError):
            check_rpc(context, time.monotonic(), 60)
        self.assertEqual(context.aborted, [grpc.StatusCode.DEADLINE_EXCEEDED])

    def test_check_rpc_aborts_when_server_cap_exceeded(self) -> None:
        """Verify that exceeding the server time cap aborts the RPC with DEADLINE_EXCEEDED."""
        context = _Context()
        with self.assertRaises(grpc.RpcError):
            check_rpc(context, time.monotonic() - 10, 1)
        self.assertEqual(context.aborted, [grpc.StatusCode.DEADLINE_EXCEEDED])

    def test_cancellation_during_page_extraction_stops_before_blocks(self) -> None:
        """Verify that cancellation during page extraction aborts before blocks are produced."""
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
        """Verify that layout extraction invokes the cancellation checkpoint once per page."""
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
        """Verify that server creation bounds worker threads, concurrent RPCs, and message sizes."""
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
        """Verify that installing shutdown handlers turns SIGTERM into a graceful stop request."""
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

    def test_launcher_parser_uses_exclusive_port(self) -> None:
        """Verify that a launcher-managed parser disables SO_REUSEPORT for an exclusive port."""
        args = SimpleNamespace(
            max_workers=4, max_concurrent_rpcs=3, grpc_max_message_size=12345
        )
        with mock.patch.dict("os.environ", {"PAPERS_PARENT_STDIN": "1"}):
            with mock.patch.object(server_module.grpc, "server") as grpc_server:
                server_module._create_server(args)
        self.assertIn(("grpc.so_reuseport", 0), grpc_server.call_args.kwargs["options"])

    def test_launcher_parent_eof_requests_graceful_shutdown(self) -> None:
        """Verify that launcher mode requests graceful shutdown when the parent closes stdin."""
        previous_term = signal.getsignal(signal.SIGTERM)
        previous_int = signal.getsignal(signal.SIGINT)
        try:
            with mock.patch.dict("os.environ", {"PAPERS_PARENT_STDIN": "1"}):
                with mock.patch.object(server_module.os, "read", return_value=b""):
                    requested = server_module._install_shutdown_handlers()
                    self.assertTrue(requested.wait(timeout=2))
        finally:
            signal.signal(signal.SIGTERM, previous_term)
            signal.signal(signal.SIGINT, previous_int)

    def test_max_concurrent_rpcs_rejects_excess_requests(self) -> None:
        """Verify that the RPC cap returns RESOURCE_EXHAUSTED while a call is still in flight."""
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
        """Verify that a stop after the grace period terminates an in-flight RPC without hanging."""
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



# ---------------------------------------------------------------------------
# Consolidated from test_runtime_operations.py.
# ---------------------------------------------------------------------------

BUNDLE_ROOT = PROJECT_ROOT / "pdf_parser" / "paper_crf"
BUNDLE_WEIGHTS = BUNDLE_ROOT / "weights" / "reference-text.crfsuite"
BUNDLE_HEADER_WEIGHTS = BUNDLE_ROOT / "weights" / "header-text-title-priors.crfsuite"
BUNDLE_CITATION_WEIGHTS = BUNDLE_ROOT / "weights" / "citation-context.crfsuite"
BUNDLE_HEADER_AND_CITATION = BUNDLE_HEADER_WEIGHTS.is_file() and BUNDLE_CITATION_WEIGHTS.is_file()


def _write_bundle(root: Path) -> Path:
    """Create a minimal bundle layout and return the header manifest path."""
    weights = root / "weights"
    specs = root / "specs"
    weights.mkdir(parents=True)
    specs.mkdir(parents=True)
    for name in ("header", "reference", "citation"):
        (weights / f"{name}.crfsuite").write_bytes(b"unused; manifest validation runs first")
    for name, feature_version in (
        ("reference", "paper-line-crf/v2"),
        ("citation", "paper-citation-crf/v1"),
    ):
        (specs / f"{name}.json").write_text(
            json.dumps({
                "schema_version": "paper-crf-artifact/v1",
                "feature_version": feature_version,
            }),
            encoding="utf-8",
        )
    header = specs / "header.json"
    (specs / "bundle.json").write_text(
        json.dumps({
            "schema_version": "paper-crf-bundle/v1",
            "models": {
                "header": {"weights": "weights/header.crfsuite", "spec": "specs/header.json"},
                "reference": {"weights": "weights/reference.crfsuite", "spec": "specs/reference.json"},
                "citation": {"weights": "weights/citation.crfsuite", "spec": "specs/citation.json"},
            },
            "runtime": {
                "header_threshold": 0.912,
                "reference_threshold": 0.946,
                "header_use_layout": False,
                "reference_use_layout": False,
            },
        }),
        encoding="utf-8",
    )
    return header


class RuntimeEngineFallbackTest(unittest.TestCase):
    """Missing artifacts must only make explicit CRF mode fail closed."""

    def test_rules_mode_does_not_require_artifacts(self) -> None:
        """Verify that rules mode builds without any CRF artifacts."""
        with TemporaryDirectory() as temporary:
            engine = _build_extraction_engine("rules", Path(temporary))
        self.assertIsInstance(engine, DeterministicPaperExtractionEngine)

    def test_relative_model_directory_is_resolved_from_repository_root(self) -> None:
        """Verify that a relative model directory resolves against the repository root."""
        self.assertEqual(
            _resolve_project_path("./pdf_parser/paper_crf"),
            BUNDLE_ROOT,
        )

    def test_relative_upload_directory_is_resolved_from_repository_root(self) -> None:
        """Verify that a relative upload directory resolves against the repository root."""
        self.assertEqual(
            _resolve_project_path("./data/uploads"),
            PROJECT_ROOT / "data" / "uploads",
        )

    def test_hybrid_and_shadow_fall_back_to_rules_when_artifacts_are_missing(self) -> None:
        """Verify that hybrid and shadow modes fall back to rules when artifacts are missing."""
        with TemporaryDirectory() as temporary:
            for mode in ("hybrid", "shadow"):
                with self.subTest(mode=mode):
                    engine = _build_extraction_engine(mode, Path(temporary))
                    self.assertIsInstance(engine, DeterministicPaperExtractionEngine)

    def test_explicit_crf_mode_rejects_missing_artifacts(self) -> None:
        """Verify that explicitly requested CRF mode fails when the bundle manifest is missing."""
        with TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "bundle manifest is missing"):
                _build_extraction_engine("crf", Path(temporary))

    def test_hybrid_falls_back_but_crf_rejects_corrupt_or_incompatible_manifests(self) -> None:
        """Verify that corrupt or incompatible manifests fall back in hybrid mode but fail CRF mode."""
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest = _write_bundle(root)
            for contents, message in (
                ("{not-json", "corrupt"),
                ('{"schema_version":"paper-crf-artifact/v1","feature_version":"wrong"}', "mismatch"),
            ):
                with self.subTest(kind=message):
                    manifest.write_text(contents, encoding="utf-8")
                    self.assertIsInstance(
                        _build_extraction_engine("hybrid", root),
                        DeterministicPaperExtractionEngine,
                    )
                    with self.assertRaises(Exception):
                        _build_extraction_engine("crf", root)


class PackagedBundleTest(unittest.TestCase):
    """The checked-out release bundle must load through the same runtime path."""

    @unittest.skipUnless(BUNDLE_WEIGHTS.is_file(), "packaged CRF weights are not present")
    def test_packaged_bundle_loads_and_builds_crf_engine(self) -> None:
        """Verify that the packaged bundle loads all models and builds a CRF engine."""
        from paper_crf import load_bundle

        bundle = load_bundle(BUNDLE_ROOT)
        self.assertEqual(set(bundle.models), {"header", "reference", "citation"})
        for selection in bundle.models.values():
            self.assertTrue(selection.weights.is_file())
            self.assertTrue(selection.spec.is_file())
        engine = _build_extraction_engine("crf", BUNDLE_ROOT)
        self.assertNotIsInstance(engine, DeterministicPaperExtractionEngine)

    @unittest.skipUnless(BUNDLE_HEADER_AND_CITATION, "packaged CRF weights are not present")
    def test_reference_crf_is_disabled_by_default(self) -> None:
        """Verify that the reference CRF stays disabled by default and is absent from the model summary."""
        engine = _build_extraction_engine("hybrid", BUNDLE_ROOT)
        self.assertFalse(engine._crf.reference_enabled)
        self.assertNotIn("reference_model", engine._crf.model_summary())

    @unittest.skipUnless(
        BUNDLE_HEADER_AND_CITATION and BUNDLE_WEIGHTS.is_file(),
        "packaged CRF weights are not present",
    )
    def test_reference_crf_can_be_enabled(self) -> None:
        """Verify that explicitly enabling the reference CRF exposes it in the model summary."""
        engine = _build_extraction_engine(
            "hybrid", BUNDLE_ROOT, reference_crf_enabled=True
        )
        self.assertTrue(engine._crf.reference_enabled)
        self.assertIn("reference_model", engine._crf.model_summary())

    @unittest.skipUnless(BUNDLE_HEADER_AND_CITATION, "packaged CRF weights are not present")
    def test_hybrid_uses_rule_references_by_default(self) -> None:
        """Verify that hybrid extraction uses rule-owned references by default."""
        markdown = (
            "# A Paper\n\n## References\n\n"
            "[1] A. Smith. A study of things. Journal, 2020.\n"
            "[2] B. Jones. Another study. Journal, 2021.\n"
        )
        lines = [
            LayoutLine(
                page_number=1, order_in_page=index, order_in_document=index,
                text=text, page_width=600.0, page_height=800.0,
                bbox_x0=50.0, bbox_y0=50.0 + index * 10,
                bbox_x1=550.0, bbox_y1=60.0 + index * 10,
            )
            for index, text in enumerate((
                "A Paper", "References",
                "[1] A. Smith. A study of things. Journal, 2020.",
                "[2] B. Jones. Another study. Journal, 2021.",
            ))
        ]
        rules = DeterministicPaperExtractionEngine().extract(markdown, (), lines)
        hybrid = _build_extraction_engine("hybrid", BUNDLE_ROOT).extract(markdown, (), lines)
        self.assertEqual(hybrid.references, rules.references)
        self.assertEqual(len(hybrid.reference_candidates), len(rules.references))


class ParserLoggerFallbackTest(unittest.TestCase):
    """A read-only or invalid file target must degrade to container stdout."""

    def test_file_handler_falls_back_when_log_parent_is_a_file(self) -> None:
        """Verify that an unusable log parent path falls back to a stream handler."""
        with TemporaryDirectory() as temporary:
            blocking_parent = Path(temporary) / "not-a-directory"
            blocking_parent.write_text("blocked", encoding="utf-8")
            with patch.dict(os.environ, {"LOG_PATH": str(blocking_parent / "today.log")}, clear=False):
                handler = logger._build_handler(logging.INFO)
        try:
            self.assertNotIsInstance(handler, logging.FileHandler)
            self.assertIsInstance(handler, logging.StreamHandler)
        finally:
            handler.close()
