"""Bounds for PDF page size, crops, response bytes, and cleaner memory (P1-8)."""
from __future__ import annotations

import io
import tempfile
import unittest
import uuid
from array import array
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import grpc
import pymupdf

import document_parser_pb2
from papers_parser.markdown_cleaner import clean_markdown_page
from papers_parser.pdf2md_service import DocumentParser, ParserLimitExceeded


class PdfLimitTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.upload_dir = Path(self.tempdir.name)

    def _parser(self, **overrides) -> DocumentParser:
        options = {
            "max_pages": 10,
            "max_page_edge_points": 5000.0,
            "max_crop_pixels": 100_000,
            "max_crops_per_document": 5,
            "max_crop_bytes": 1 << 20,
            "max_markdown_bytes": 1 << 20,
            "max_response_bytes": 1 << 20,
        }
        options.update(overrides)
        return DocumentParser(self.upload_dir, 1 << 20, 72, **options)

    def _write_pdf(self, pages: int, width: float = 200.0, height: float = 200.0) -> Path:
        path = self.upload_dir / f"limits-{pages}-{width}-{height}.pdf"
        document = pymupdf.open()
        for _ in range(pages):
            document.new_page(width=width, height=height)
        document.save(str(path))
        document.close()
        return path

    def _check_limits(self, path: Path, parser: DocumentParser) -> None:
        with pymupdf.open(str(path)) as document:
            parser._check_pdf_limits(document)

    def test_rejects_too_many_pages(self) -> None:
        path = self._write_pdf(pages=3)
        with self.assertRaises(ParserLimitExceeded):
            self._check_limits(path, self._parser(max_pages=2))

    def test_rejects_oversized_media_box(self) -> None:
        path = self._write_pdf(pages=1, width=20_000, height=20_000)
        with self.assertRaises(ParserLimitExceeded):
            self._check_limits(path, self._parser(max_page_edge_points=5000))

    def test_accepts_normal_pdf(self) -> None:
        path = self._write_pdf(pages=2)
        self._check_limits(path, self._parser())

    def test_crop_is_downsampled_to_pixel_budget(self) -> None:
        document = pymupdf.open()
        page = document.new_page(width=1000, height=1000)
        data = self._parser(max_crop_pixels=100_000)._render_crop(
            page, (0, 0, 1000, 1000)
        )
        pixmap = pymupdf.Pixmap(io.BytesIO(data))
        self.assertGreater(pixmap.width, 0)
        self.assertGreater(pixmap.height, 0)
        self.assertLessEqual(pixmap.width * pixmap.height, 100_000)
        document.close()

    def test_response_budget_estimation(self) -> None:
        blocks = [SimpleNamespace(text="block", image_data=b"x" * 32)]
        with self.assertRaises(ParserLimitExceeded):
            self._parser(max_response_bytes=16)._check_response_budget(b"markdown", blocks)

    def _extract_with_boxes(self, parser: DocumentParser, boxes: int, crop: bytes) -> None:
        path = self._write_pdf(pages=1)
        page_boxes = [
            {"class": "image", "bbox": [0, 0, 10, 10], "pos": [0, 0]}
            for _ in range(boxes)
        ]
        pages = [{"text": "", "page_boxes": page_boxes}]
        cleaned = [clean_markdown_page("")]
        with pymupdf.open(str(path)) as document:
            with mock.patch.object(parser, "_render_crop", return_value=crop):
                parser._extract_blocks(document, pages, [""], cleaned)

    def test_crop_count_budget_aborts(self) -> None:
        parser = self._parser(max_crops_per_document=2)
        with self.assertRaises(ParserLimitExceeded):
            self._extract_with_boxes(parser, boxes=3, crop=b"x")

    def test_crop_byte_budget_aborts(self) -> None:
        parser = self._parser(max_crop_bytes=10)
        with self.assertRaises(ParserLimitExceeded):
            self._extract_with_boxes(parser, boxes=2, crop=b"x" * 8)

    def test_cleaner_offset_map_uses_compact_array(self) -> None:
        result = clean_markdown_page("# Title\n\nBody")
        self.assertIsInstance(result.offset_map, array)
        self.assertEqual(result.map_range(0, len("# Title")), (0, len("# Title")))

    def _text_pdf(self, text: str) -> bytes:
        document = pymupdf.open()
        page = document.new_page()
        page.insert_text((72, 72), text)
        data = document.tobytes()
        document.close()
        return data

    def test_recognize_content_parses_without_temp_file(self) -> None:
        parser = self._parser()
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()),
            filename="memory.pdf",
            content_type="application/pdf",
            content=self._text_pdf("Memory parser text"),
        )
        with mock.patch(
            "tempfile.mkstemp",
            side_effect=AssertionError("content recognition must not create a temp file"),
        ):
            result = parser.Recognize(request, _RecordingContext())
        self.assertIn(b"Memory parser text", result.markdown)
        self.assertEqual(result.page_count, 1)

    def test_recognize_file_path_still_works(self) -> None:
        path = self.upload_dir / "path-mode.pdf"
        path.write_bytes(self._text_pdf("Path parser text"))
        parser = self._parser()
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()),
            filename="path.pdf",
            content_type="application/pdf",
            file_path=str(path),
        )
        result = parser.Recognize(request, _RecordingContext())
        self.assertIn(b"Path parser text", result.markdown)


class _RecordingContext:
    """Minimal grpc.ServicerContext used by direct Recognize calls."""

    def abort(self, code, message):
        raise RuntimeError(f"{code}: {message}")

    def is_active(self) -> bool:
        return True

    def time_remaining(self):
        return None


if __name__ == "__main__":
    unittest.main()
