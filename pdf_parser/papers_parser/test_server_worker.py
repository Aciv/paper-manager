"""Regression tests for the lightweight server / heavy worker split."""
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
from papers_parser.worker_pool import WorkerPool, WorkerPoolConfig
from papers_parser.worker_protocol import METHOD_EXTRACT, METHOD_RECOGNIZE, METHOD_RENDER


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


class ServerImportTest(unittest.TestCase):
    def test_server_import_does_not_load_heavy_parser_dependencies(self) -> None:
        parser_dir = Path(__file__).resolve().parents[1]
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
        parser_dir = Path(__file__).resolve().parents[1]
        env = os.environ.copy()
        env["PYTHONPATH"] = str(parser_dir) + os.pathsep + env.get("PYTHONPATH", "")
        script = (
            "import sys, pathlib; "
            "from papers_parser.engine_factory import build_extraction_engine; "
            "build_extraction_engine('rules', pathlib.Path('/tmp')); "
            "heavy=[name for name in ('pymupdf4llm','pymupdf_layout','onnxruntime','pycrfsuite','model_train') if name in sys.modules]; "
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


if __name__ == "__main__":
    unittest.main()
