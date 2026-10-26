"""PDF recognition: Markdown cleaning, parser limits, RPC integration, source outlines, and page rendering.

Consolidated parser test module; see the per-test docstrings for the exact contract.
"""

from __future__ import annotations

import unittest
from papers_parser.markdown_cleaner import clean_markdown_page
import io
import tempfile
import uuid
from array import array
from pathlib import Path
from types import SimpleNamespace
from unittest import mock
import grpc
import pymupdf
import document_parser_pb2
from papers_parser.pdf2md_service import DocumentParser, ParserLimitExceeded
import threading
from concurrent import futures
import pymupdf4llm
import document_parser_pb2_grpc
from papers_parser.pdf2md_service import DocumentParser
from papers_parser.source_outline import select_source_outline
from papers_parser.render_pdf_region import (
    RegionRenderError,
    render_page_image,
    render_region,
    resolve_storage_source,
)

# ---------------------------------------------------------------------------
# Consolidated from test_markdown_cleaner.py.
# ---------------------------------------------------------------------------

class MarkdownCleanerTest(unittest.TestCase):
    def test_removes_known_noise_and_preserves_markdown_structure(self) -> None:
        """Verify that known noise is stripped while headings, fences, tables, and list structure survive."""
        raw = """1
# A


**==> picture [x] intentionally omitted <==**
<br>
```
x
```
|---|
- A
$$
\ufffdvalid\x00 text\x20\x20\x20
2
"""

        result = clean_markdown_page(raw)

        self.assertEqual(
            result.text,
            "# A\n\n```\nx\n```\n|---|\n- A\n$$\nvalid text\n",
        )
        self.assertGreaterEqual(result.removed_lines, 4)
        self.assertGreater(result.removed_characters, 0)

    def test_maps_ranges_after_a_removed_line(self) -> None:
        """Verify that source ranges map correctly onto cleaned text after an earlier noise line is removed."""
        raw = "**==> picture [x] intentionally omitted <==**\n# **Title**\n"
        result = clean_markdown_page(raw)
        raw_start = raw.index("#")
        raw_end = len(raw) - 1

        self.assertEqual(result.text, "# **Title**\n")
        self.assertEqual(result.map_range(raw_start, raw_end), (0, len("# **Title**")))

    def test_keeps_a_short_heading_and_non_edge_numeric_content(self) -> None:
        """Verify that a short heading and an interior numeric line are not discarded as page noise."""
        raw = "# A\n\n2\n\nBody\n"

        result = clean_markdown_page(raw)

        self.assertIn("# A", result.text)
        self.assertIn("\n2\n", result.text)



# ---------------------------------------------------------------------------
# Consolidated from test_pdf_limits.py.
# ---------------------------------------------------------------------------

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
        """Verify that recognition rejects PDFs whose page count exceeds the configured maximum."""
        path = self._write_pdf(pages=3)
        with self.assertRaises(ParserLimitExceeded):
            self._check_limits(path, self._parser(max_pages=2))

    def test_rejects_oversized_media_box(self) -> None:
        """Verify that a page whose media box exceeds the edge-point budget is rejected."""
        path = self._write_pdf(pages=1, width=20_000, height=20_000)
        with self.assertRaises(ParserLimitExceeded):
            self._check_limits(path, self._parser(max_page_edge_points=5000))

    def test_accepts_normal_pdf(self) -> None:
        """Verify that a PDF within every configured limit passes validation."""
        path = self._write_pdf(pages=2)
        self._check_limits(path, self._parser())

    def test_crop_is_downsampled_to_pixel_budget(self) -> None:
        """Verify that oversized crops are downsampled to stay within the pixel budget."""
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
        """Verify that the response budget check rejects blocks whose estimated payload exceeds the limit."""
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
        """Verify that exceeding the per-document crop count aborts block extraction."""
        parser = self._parser(max_crops_per_document=2)
        with self.assertRaises(ParserLimitExceeded):
            self._extract_with_boxes(parser, boxes=3, crop=b"x")

    def test_crop_byte_budget_aborts(self) -> None:
        """Verify that exceeding the per-document crop byte budget aborts block extraction."""
        parser = self._parser(max_crop_bytes=10)
        with self.assertRaises(ParserLimitExceeded):
            self._extract_with_boxes(parser, boxes=2, crop=b"x" * 8)

    def test_cleaner_offset_map_uses_compact_array(self) -> None:
        """Verify that the cleaner's offset map is a compact array with correct cleaned-range mappings."""
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
        """Verify that in-memory content recognition returns Markdown without creating a temporary file."""
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

    def test_recognize_rejects_unreadable_pdf(self) -> None:
        """Verify that malformed PDF bytes are rejected as an internal recognition error."""
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()),
            filename="broken.pdf",
            content_type="application/pdf",
            content=b"%PDF-1.7\nnot a readable document",
        )
        with self.assertRaisesRegex(RuntimeError, "StatusCode.INTERNAL"):
            self._parser().Recognize(request, _RecordingContext())

    def test_recognize_rejects_encrypted_pdf_without_password(self) -> None:
        """Verify that an encrypted PDF without a password is rejected during recognition."""
        path = self.upload_dir / "encrypted.pdf"
        document = pymupdf.open()
        document.new_page().insert_text((72, 72), "secret")
        document.save(
            str(path),
            encryption=pymupdf.PDF_ENCRYPT_AES_256,
            owner_pw="owner-password",
            user_pw="user-password",
        )
        document.close()
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()),
            filename="encrypted.pdf",
            content_type="application/pdf",
            content=path.read_bytes(),
        )
        with self.assertRaisesRegex(RuntimeError, "StatusCode.INTERNAL"):
            self._parser().Recognize(request, _RecordingContext())

    def test_recognize_file_path_still_works(self) -> None:
        """Verify that file-path recognition still extracts Markdown from the saved PDF."""
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

    def test_recognize_returns_native_outline_with_same_generation(self) -> None:
        """Verify that recognition returns the native PDF outline with its sections and blocks."""
        document = pymupdf.open()
        first = document.new_page()
        first.insert_text((72, 72), "Introduction")
        second = document.new_page()
        second.insert_text((72, 72), "Results")
        document.set_toc([[1, "Introduction", 1], [1, "Results", 2]])
        content = document.tobytes()
        document.close()
        request = document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()),
            filename="outlined.pdf",
            content_type="application/pdf",
            content=content,
        )
        result = self._parser().Recognize(request, _RecordingContext())
        self.assertEqual(result.outline.schema_version, 1)
        self.assertEqual(result.outline.source, "pdf_outline")
        self.assertEqual([section.title for section in result.outline.sections], ["Introduction", "Results"])
        self.assertGreaterEqual(len(result.blocks), 2)


class _RecordingContext:
    """Minimal grpc.ServicerContext used by direct Recognize calls."""

    def abort(self, code, message):
        raise RuntimeError(f"{code}: {message}")

    def is_active(self) -> bool:
        return True

    def time_remaining(self):
        return None



# ---------------------------------------------------------------------------
# Consolidated from test_recognition_rpc_integration.py.
# ---------------------------------------------------------------------------

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
        """Verify that a gRPC recognition call returns the page count, Markdown, blocks, and a Markdown filename."""
        server = self.start()
        result = server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(result.page_count, 2)
        self.assertIn("graph neural networks", result.markdown.decode("utf-8").lower())
        self.assertGreaterEqual(len(result.blocks), 1)
        self.assertTrue(result.markdown_filename.endswith(".md"))

    def test_source_over_size_limit_is_rejected(self) -> None:
        """Verify that an oversized PDF is rejected over gRPC with RESOURCE_EXHAUSTED."""
        server = self.start(max_file_size=len(self.pdf) - 1)
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def key_request(self, key, **kwargs):
        return document_parser_pb2.RecognitionRequest(
            document_id=str(uuid.uuid4()), filename="paper.pdf", content_type="application/pdf",
            source_storage_key=key, **kwargs)

    def test_storage_key_reads_saved_pdf_without_content_or_temp_file(self):
        """Verify that a storage-key request reads the saved PDF in place without content or a temp file."""
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
        """Verify that a missing storage-key source returns NOT_FOUND over gRPC."""
        server = self.start()
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(self.key_request("pdf/missing.pdf"), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.NOT_FOUND)

    def test_storage_key_rejects_paths_and_symlink_escape(self):
        """Verify that traversal, absolute, and symlink storage keys are rejected without escaping the upload root."""
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
        """Verify that storage-key requests still enforce declared and actual source size limits."""
        (self.upload_dir / "source.pdf").write_bytes(self.pdf)
        for maximum, bound in ((len(self.pdf)-1, len(self.pdf)), (len(self.pdf)+1, len(self.pdf)-1)):
            server = self.start(max_file_size=maximum)
            with self.assertRaises(grpc.RpcError) as raised:
                server.stub.Recognize(self.key_request("source.pdf", source_size_bytes=bound), timeout=30)
            self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def test_page_limit_is_enforced(self) -> None:
        """Verify that a PDF over the page limit is rejected over gRPC with RESOURCE_EXHAUSTED."""
        server = self.start(max_pages=1)
        with self.assertRaises(grpc.RpcError) as raised:
            server.stub.Recognize(recognition_request(self.pdf), timeout=30)
        self.assertEqual(raised.exception.code(), grpc.StatusCode.RESOURCE_EXHAUSTED)

    def test_deadline_is_returned_to_the_client(self) -> None:
        """Verify that the client receives DEADLINE_EXCEEDED when recognition outlives the RPC deadline."""
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
        """Verify that the RPC cap rejects excess concurrent work while the first call completes."""
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



# ---------------------------------------------------------------------------
# Consolidated from test_source_outline.py.
# ---------------------------------------------------------------------------

def block(ordinal: int, page: int, start: int, end: int, y: float):
    return document_parser_pb2.RecognitionBlock(
        ordinal=ordinal,
        type="text",
        text="block",
        page_number=page,
        page_width=600,
        page_height=800,
        bbox_x0=50,
        bbox_y0=y,
        bbox_x1=550,
        bbox_y1=y + 20,
        markdown_codepoint_start=start,
        markdown_codepoint_end=end,
    )


class FakePDF:
    def __init__(self, toc, pages: int = 2):
        self.toc = toc
        self.pages = [SimpleNamespace(rect=SimpleNamespace(width=600, height=800)) for _ in range(pages)]

    def get_toc(self, simple=False):
        return self.toc

    def __len__(self):
        return len(self.pages)

    def __getitem__(self, index):
        return self.pages[index]


class SourceOutlineTest(unittest.TestCase):
    def test_native_outline_wins_and_preserves_hierarchy(self):
        """Verify that a valid native TOC wins and preserves parent-child section ordinals."""
        pdf = FakePDF([
            [1, "Introduction", 1, {"to": SimpleNamespace(x=50, y=90)}],
            [2, "Method", 1, {"to": SimpleNamespace(x=50, y=290)}],
            [1, "Results", 2, {"to": SimpleNamespace(x=50, y=100)}],
        ])
        markdown = "# ignored fallback\n" + "x" * 80
        result = select_source_outline(pdf, markdown, [
            block(0, 1, 0, 20, 80), block(1, 1, 21, 50, 280), block(2, 2, 51, 90, 90),
        ])
        self.assertEqual(result.source, "pdf_outline")
        self.assertEqual([item.parent_ordinal for item in result.sections], [-1, 0, -1])
        self.assertEqual(result.sections[1].start_block_ordinal, 1)

    def test_wholly_invalid_native_outline_falls_back_to_clean_markdown(self):
        """Verify that an unusable native outline falls back to clean Markdown headings with diagnostics."""
        pdf = FakePDF([[1, "Broken", 99, {}]])
        markdown = "# 介绍\n正文\n\n```md\n# not a heading\n```\n\nMethods\n-------\nbody"
        result = select_source_outline(pdf, markdown, [
            block(0, 1, 0, 10, 70), block(1, 2, 45, len(markdown), 70),
        ])
        self.assertEqual(result.source, "markdown_headings")
        self.assertEqual([item.title for item in result.sections], ["介绍", "Methods"])
        self.assertEqual(result.sections[0].markdown_codepoint_start, 0)
        self.assertIn("pdf_outline_wholly_unusable", result.diagnostics)

    def test_markdown_fallback_removes_emphasis_wrappers_from_titles(self):
        """Verify that Markdown heading fallback strips emphasis wrappers from section titles."""
        markdown = (
            "# **Introduction**\n"
            "## **1.1** **Method**\n"
            "## **C. Statistics** **Bench**\n"
        )
        result = select_source_outline(FakePDF([]), markdown, [])
        self.assertEqual(result.source, "markdown_headings")
        self.assertEqual(
            [item.title for item in result.sections],
            ["Introduction", "1.1 Method", "C. Statistics Bench"],
        )

    def test_missing_everything_returns_nonfatal_none(self):
        """Verify that a document without an outline returns a nonfatal empty none result."""
        result = select_source_outline(FakePDF([]), "plain text", [])
        self.assertEqual(result.source, "none")
        self.assertEqual(list(result.sections), [])
        self.assertIn("outline_unavailable", result.diagnostics)

    def test_page_only_duplicate_titles_are_retained_and_item_count_is_bounded(self):
        """Verify that page-only TOC entries are retained up to the item cap with a limit diagnostic."""
        toc = [[1, "Repeated", 1, {}] for _ in range(520)]
        result = select_source_outline(FakePDF(toc, pages=1), "body", [])
        self.assertEqual(result.source, "pdf_outline")
        self.assertEqual(len(result.sections), 512)
        self.assertTrue(all(section.title == "Repeated" for section in result.sections))
        self.assertTrue(all(section.start_page == 1 for section in result.sections))
        self.assertTrue(all(section.start_block_ordinal == -1 for section in result.sections))
        self.assertIn("pdf_outline_item_limit", result.diagnostics)

    def test_rotated_page_native_destination_remains_bounded(self):
        """Verify that a native destination on a rotated page still yields a bounded, existing block ordinal."""
        document = pymupdf.open()
        page = document.new_page(width=600, height=800)
        page.insert_text((72, 72), "Rotated")
        page.set_rotation(90)
        document.set_toc([[1, "Rotated", 1]])
        result = select_source_outline(document, "# Rotated\n", [block(0, 1, 0, 10, 60)])
        document.close()
        self.assertEqual(result.source, "pdf_outline")
        self.assertEqual(result.sections[0].start_page, 1)
        self.assertGreaterEqual(result.sections[0].start_block_ordinal, 0)



# ---------------------------------------------------------------------------
# Consolidated from test_render_pdf_region.py.
# ---------------------------------------------------------------------------

class RenderPDFRegionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.source = Path(self.tempdir.name) / "source.pdf"
        document = pymupdf.open()
        page = document.new_page(width=300, height=200)
        page.draw_rect((20, 30, 220, 130), color=(1, 0, 0), fill=(1, 1, 1))
        page.insert_text((30, 60), "bounded region")
        document.save(str(self.source))
        document.close()

    def test_renders_requested_bbox_as_png(self) -> None:
        """Verify that rendering a requested PDF rectangle returns a PNG at the expected pixel size."""
        output = render_region(self.source, 1, (20, 30, 220, 130), 144, 300, 1_000_000)
        pixmap = pymupdf.Pixmap(io.BytesIO(output))
        self.assertEqual((pixmap.width, pixmap.height), (400, 200))

    def test_downsamples_to_pixel_budget(self) -> None:
        """Verify that region rendering downsamples output to stay within the pixel budget."""
        output = render_region(self.source, 1, (0, 0, 300, 200), 300, 300, 20_000)
        pixmap = pymupdf.Pixmap(io.BytesIO(output))
        self.assertLessEqual(pixmap.width * pixmap.height, 20_000)

    def test_rejects_page_and_bbox_outside_document(self) -> None:
        """Verify that out-of-range page numbers and bounding boxes raise RegionRenderError."""
        with self.assertRaises(RegionRenderError):
            render_region(self.source, 2, (0, 0, 10, 10), 144, 300, 1_000_000)
        with self.assertRaises(RegionRenderError):
            render_region(self.source, 1, (0, 0, 301, 10), 144, 300, 1_000_000)

    def test_renders_full_page_and_normalized_region(self) -> None:
        """Verify that full-page and normalized-region renders return PNGs with the expected dimensions."""
        full_png, width, height, page_width, page_height = render_page_image(
            self.source, 1, normalized_bbox=None, full_page=True,
            dpi=72, max_dpi=300, max_pixels=1_000_000,
        )
        self.assertTrue(full_png.startswith(b"\x89PNG\r\n\x1a\n"))
        self.assertEqual((width, height), (300, 200))
        self.assertEqual((page_width, page_height), (300, 200))
        region_png, region_width, region_height, _, _ = render_page_image(
            self.source, 1, normalized_bbox=(0.5, 0.5, 1.0, 1.0), full_page=False,
            dpi=72, max_dpi=300, max_pixels=1_000_000,
        )
        self.assertTrue(region_png)
        self.assertEqual((region_width, region_height), (150, 100))

    def test_renders_rotated_page(self) -> None:
        """Verify that full-page rendering handles a rotated page and returns positive dimensions."""
        document = pymupdf.open()
        page = document.new_page(width=300, height=200)
        page.insert_text((30, 60), "rotated")
        page.set_rotation(90)
        document.save(str(self.source))
        document.close()
        png, width, height, _, _ = render_page_image(
            self.source, 1, normalized_bbox=None, full_page=True,
            dpi=72, max_dpi=300, max_pixels=1_000_000,
        )
        self.assertTrue(png)
        self.assertGreater(width, 0)
        self.assertGreater(height, 0)

    def test_storage_key_cannot_escape_upload_root(self) -> None:
        """Verify that storage keys resolve only inside the upload root and reject traversal or absolute paths."""
        root = Path(self.tempdir.name)
        self.assertEqual(resolve_storage_source(root, "source.pdf"), self.source.resolve())
        with self.assertRaises(RegionRenderError):
            resolve_storage_source(root, "../source.pdf")
        with self.assertRaises(RegionRenderError):
            resolve_storage_source(root, str(self.source))
