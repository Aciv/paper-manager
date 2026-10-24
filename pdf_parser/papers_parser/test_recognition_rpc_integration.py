"""Real gRPC integration tests for DocumentParser.Recognize.

These start an in-process gRPC server (the same servicer and concurrency
settings used by server.py) instead of calling the Python method directly, so
deadline, size, and concurrency behavior is exercised over the wire.
"""
from __future__ import annotations

import tempfile
import threading
import unittest
import uuid
from concurrent import futures
from pathlib import Path
from unittest import mock

import grpc
import pymupdf
import pymupdf4llm

import document_parser_pb2
import document_parser_pb2_grpc
from papers_parser.pdf2md_service import DocumentParser


def make_pdf(pages: int = 2, lines_per_page: int = 6) -> bytes:
    document = pymupdf.open()
    for page_index in range(pages):
        page = document.new_page()
        for line_index in range(lines_per_page):
            page.insert_text(
                (50, 60 + line_index * 16),
                f"Page {page_index + 1} line {line_index + 1}: "
                f"graph neural networks reference {page_index * lines_per_page + line_index}.",
            )
    data = document.tobytes()
    document.close()
    return data


def recognition_request(content: bytes, filename: str = "paper.pdf"):
    return document_parser_pb2.RecognitionRequest(
        document_id=str(uuid.uuid4()),
        filename=filename,
        content_type="application/pdf",
        content=content,
    )


class ParserRPCServer:
    def __init__(self, servicer: DocumentParser, max_concurrent_rpcs: int = 2):
        self.server = grpc.server(
            futures.ThreadPoolExecutor(max_workers=2),
            maximum_concurrent_rpcs=max_concurrent_rpcs,
        )
        document_parser_pb2_grpc.add_DocumentParserServicer_to_server(servicer, self.server)
        port = self.server.add_insecure_port("127.0.0.1:0")
        self.server.start()
        self.channel = grpc.insecure_channel(f"127.0.0.1:{port}")
        self.stub = document_parser_pb2_grpc.DocumentParserStub(self.channel)

    def close(self) -> None:
        self.channel.close()
        self.server.stop(grace=0).wait(timeout=5)


class RecognitionRPCIntegrationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.directory = tempfile.TemporaryDirectory()
        self.upload_dir = Path(self.directory.name)
        self.pdf = make_pdf()

    def tearDown(self) -> None:
        self.directory.cleanup()

    def start(self, *, max_file_size: int | None = None, max_pages: int = 10,
              max_concurrent_rpcs: int = 2) -> ParserRPCServer:
        servicer = DocumentParser(
            self.upload_dir,
            max_file_size if max_file_size is not None else len(self.pdf) + 1,
            72,
            max_pages=max_pages,
            max_crops_per_document=10,
        )
        server = ParserRPCServer(servicer, max_concurrent_rpcs=max_concurrent_rpcs)
        self.addCleanup(server.close)
        return server

    def test_recognize_over_grpc_returns_markdown_and_blocks(self) -> None:
        server = self.start()
        result = server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(result.page_count, 2)
        self.assertIn("graph neural networks", result.markdown.decode("utf-8").lower())
        self.assertGreaterEqual(len(result.blocks), 1)
        self.assertTrue(result.markdown_filename.endswith(".md"))

    def test_source_over_size_limit_is_rejected(self) -> None:
        server = self.start(max_file_size=len(self.pdf) - 1)
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def key_request(self, key, **kwargs):
        return document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()), filename="paper.pdf", content_type="application/pdf",
            source_storage_key=key, **kwargs)

    def test_storage_key_reads_saved_pdf_without_content_or_temp_file(self):
        folder = self.upload_dir / "pdf"
        folder.mkdir()
        (folder / "论文 source.bin").write_bytes(self.pdf)
        server = self.start()
        request = self.key_request("pdf/论文 source.bin", source_size_bytes=len(self.pdf))
        self.assertFalse(request.content)
        with mock.patch("tempfile.mkstemp", side_effect=AssertionError("unexpected temporary PDF")):
            result = server.stub.Recognize(request, timeout=30)
        self.assertEqual(result.page_count, 2)
        self.assertTrue(result.markdown)
        self.assertTrue(result.blocks)

    def test_storage_key_reports_missing_source_for_bytes_fallback(self):
        server = self.start()
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(self.key_request("pdf/missing.pdf"), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.NOT_FOUND)

    def test_storage_key_rejects_paths_and_symlink_escape(self):
        server = self.start()
        for key in ("../paper.pdf", "/paper.pdf", "pdf/../paper.pdf", "pdf//paper.pdf", "pdf/./paper.pdf", "C:/paper.pdf", "pdf\\paper.pdf"):
            with self.subTest(key=key), self.assertRaises(grpc.RpcError) as raised:
                server.stub.Recognize(self.key_request(key), timeout=30)
            self.assertEqual(raised.exception.code(), grpc.StatusCode.INVALID_ARGUMENT)
        with tempfile.TemporaryDirectory() as outside:
            target = Path(outside) / "outside.pdf"
            target.write_bytes(self.pdf)
            (self.upload_dir / "escape.pdf").symlink_to(target)
            with self.assertRaises(grpc.RpcError) as raised:
                server.stub.Recognize(self.key_request("escape.pdf"), timeout=30)
            self.assertEqual(raised.exception.code(), grpc.StatusCode.PERMISSION_DENIED)

    def test_storage_key_preserves_size_limits(self):
        (self.upload_dir / "source.pdf").write_bytes(self.pdf)
        for maximum, bound in ((len(self.pdf)-1, len(self.pdf)), (len(self.pdf)+1, len(self.pdf)-1)):
            server = self.start(max_file_size=maximum)
            with self.assertRaises(grpc.RpcError) as raised:
                server.stub.Recognize(self.key_request("source.pdf", source_size_bytes=bound), timeout=30)
            self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def test_page_limit_is_enforced(self) -> None:
        server = self.start(max_pages=1)
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def test_deadline_is_returned_to_the_client(self) -> None:
        server = self.start()
        entered = threading.Event()
        release = threading.Event()
        original = pymupdf4llm.to_markdown

        def slow_to_markdown(*args, **kwargs):
            entered.set()
            release.wait(timeout=10)
            return original(*args, **kwargs)

        with mock.patch.object(pymupdf4llm, "to_markdown", side_effect=slow_to_markdown):
            future = server.stub.Recognize.future(recognition_request(self.pdf), timeout=2)
            try:
                self.assertTrue(entered.wait(timeout=1.5))
                with self.assertRaises(grpc.RpcError) as raised:
                    future.result(timeout=5)
            finally:
                release.set()
        self.assertEqual(raised.exception.code(), grpc.StatusCode.DEADLINE_EXCEEDED)

    def test_concurrent_rpc_cap_rejects_excess_work(self) -> None:
        server = self.start(max_concurrent_rpcs=1)
        entered = threading.Event()
        release = threading.Event()
        original = pymupdf4llm.to_markdown

        def slow_to_markdown(*args, **kwargs):
            entered.set()
            release.wait(timeout=10)
            return original(*args, **kwargs)

        with mock.patch("papers_parser.pdf2md_service.pymupdf4llm.to_markdown", slow_to_markdown):
            first = server.stub.Recognize.future(recognition_request(self.pdf), timeout=30)
            self.assertTrue(entered.wait(timeout=10), "first RPC did not reach parsing")
            with self.assertRaises(grpc.RpcError) as raised:
                server.stub.Recognize(recognition_request(self.pdf), timeout=5)
            self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)
            release.set()
            self.assertEqual(first.result(timeout=30).page_count, 2)


if __name__ == "__main__":
    unittest.main()
