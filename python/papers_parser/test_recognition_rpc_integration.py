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

    def test_page_limit_is_enforced(self) -> None:
        server = self.start(max_pages=1)
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def test_deadline_is_returned_to_the_client(self) -> None:
        server = self.start()
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(recognition_request(self.pdf), timeout=0.000001)
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
