"""Focused tests for the paper extraction gRPC transport."""
from __future__ import annotations

import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from uuid import uuid4

import document_parser_pb2
import pymupdf

from papers_parser.extractor_service import (
    PaperMetadataExtractionRPC,
    _decode_extraction_request,
)
from papers_parser.paper_extractor import (
    DeterministicPaperExtractionEngine,
    ExtractionOutput,
)
from papers_parser.paper_extractor import SourceBlock, SourceLocation
from papers_parser.layout_lines import extract_layout_lines_from_storage


MAX_MARKDOWN_BYTES = 32 << 20
MAX_BLOCKS = 50_000


def _location(start: int, end: int):
    """Create one valid protobuf source location for direct-block RPC tests."""
    return document_parser_pb2.PaperSourceLocation(
        page_number=1,
        page_width=612,
        page_height=792,
        bbox_x0=72,
        bbox_y0=72,
        bbox_x1=300,
        bbox_y1=110,
        markdown_codepoint_start=start,
        markdown_codepoint_end=end,
    )


class FakeContext:
    def is_active(self) -> bool:
        return True


class LayoutRecordingEngine:
    """Minimal CRF-shaped engine used to prove RPC lazy-layout wiring."""

    requires_layout = True

    def __init__(self) -> None:
        self.received_layout = ()

    def extract(self, _markdown, _blocks, layout_lines=()):
        self.received_layout = tuple(layout_lines)
        return ExtractionOutput(extractor_name="layout-recording", extractor_version="test")


class PaperMetadataExtractionRPCTest(unittest.TestCase):
    def test_maps_bounded_request_to_deterministic_response(self) -> None:
        markdown = b"""# **RPC Paper**

### **Grace Hopper**

## Abstract

RPC extraction.

## References

[1] Raw reference.
"""
        title_start = markdown.decode().index("RPC Paper")
        title_end = title_start + len("RPC Paper")
        author_start = markdown.decode().index("Grace Hopper")
        abstract_heading_start = markdown.decode().index("Abstract")
        abstract_start = markdown.decode().index("RPC extraction.")
        references_heading_start = markdown.decode().index("References")
        reference_start = markdown.decode().index("[1] Raw reference.")
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()),
            markdown=markdown,
            blocks=[
                document_parser_pb2.PaperSourceBlock(
                    ordinal=0,
                    type="title",
                    text="RPC Paper",
                    location=_location(title_start, title_end),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=1,
                    type="text",
                    text="### **Grace Hopper**",
                    location=_location(author_start, author_start + len("Grace Hopper")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=2,
                    type="section_header",
                    text="## Abstract",
                    location=_location(abstract_heading_start, abstract_heading_start + len("Abstract")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=3,
                    type="text",
                    text="RPC extraction.",
                    location=_location(abstract_start, abstract_start + len("RPC extraction.")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=4,
                    type="section_header",
                    text="## References",
                    location=_location(references_heading_start, references_heading_start + len("References")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=5,
                    type="text",
                    text="[1] Raw reference.",
                    location=_location(reference_start, reference_start + len("[1] Raw reference.")),
                ),
            ],
        )

        response = PaperMetadataExtractionRPC(
            DeterministicPaperExtractionEngine(),
            MAX_MARKDOWN_BYTES,
            MAX_BLOCKS,
        ).Extract(request, FakeContext())

        self.assertEqual(response.title.value, "RPC Paper")
        self.assertEqual(response.title.evidence[0].block_ordinal, 0)
        self.assertEqual(response.authors[0].value, "Grace Hopper")
        self.assertEqual(response.abstract.value, "RPC extraction.")
        self.assertTrue(response.references_detected)
        self.assertEqual(list(response.references), ["[1] Raw reference."])
        self.assertEqual(response.reference_candidates[0].raw_reference, "[1] Raw reference.")
        self.assertEqual(response.extractor_name, "markdown-rules")

    def test_accepts_request_without_markdown_checksum(self) -> None:
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()),
            markdown=b"# Paper",
        )
        markdown, blocks, source_storage_key = _decode_extraction_request(
            request,
            MAX_MARKDOWN_BYTES,
            MAX_BLOCKS,
        )
        self.assertEqual(markdown, "# Paper")
        self.assertEqual(blocks, ())
        self.assertEqual(source_storage_key, "")

    def test_decodes_source_storage_key(self) -> None:
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()), markdown=b"# Paper\n",
            source_storage_key="pdf/source.pdf",
        )
        _, _, source_storage_key = _decode_extraction_request(request, MAX_MARKDOWN_BYTES, MAX_BLOCKS)
        self.assertEqual(source_storage_key, "pdf/source.pdf")

    def test_rejects_oversized_source_storage_key(self) -> None:
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()), markdown=b"# Paper\n",
            source_storage_key="x" * 1025,
        )
        with self.assertRaisesRegex(ValueError, "source storage key"):
            _decode_extraction_request(request, MAX_MARKDOWN_BYTES, MAX_BLOCKS)

    def test_derives_lazy_layout_only_from_persisted_block_rectangles(self) -> None:
        """The CRF-only layout input is rebuilt from the shared PDF on demand."""
        with TemporaryDirectory() as directory:
            root = Path(directory)
            pdf_dir = root / "pdf"
            pdf_dir.mkdir()
            source = pdf_dir / "paper.pdf"
            with pymupdf.open() as document:
                page = document.new_page(width=612, height=792)
                page.insert_text((72, 96), "A Scoped Title", fontsize=18)
                page.insert_text((72, 180), "Outside semantic block", fontsize=10)
                document.save(source)

            location = SourceLocation(
                page_number=1,
                page_width=612,
                page_height=792,
                bbox_x0=60,
                bbox_y0=70,
                bbox_x1=300,
                bbox_y1=115,
                markdown_codepoint_start=0,
                markdown_codepoint_end=len("A Scoped Title"),
            )
            lines = extract_layout_lines_from_storage(
                root,
                "pdf/paper.pdf",
                (SourceBlock(ordinal=7, type="title", text="A Scoped Title", location=location),),
                10,
            )

        self.assertEqual(len(lines), 1)
        self.assertEqual(lines[0].text, "A Scoped Title")
        self.assertEqual(lines[0].parent_block_ordinal, 7)
        self.assertEqual(lines[0].markdown_codepoint_start, 0)
        self.assertEqual(lines[0].markdown_codepoint_end, len("A Scoped Title"))

    def test_rejects_lazy_layout_storage_path_escape(self) -> None:
        with TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "outside the parser upload directory"):
                extract_layout_lines_from_storage(
                    Path(directory), "../outside.pdf", (), 10,
                )

    def test_rpc_lazily_supplies_layout_to_layout_aware_engine(self) -> None:
        with TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "pdf").mkdir()
            source = root / "pdf" / "paper.pdf"
            with pymupdf.open() as document:
                page = document.new_page(width=612, height=792)
                page.insert_text((72, 96), "A Scoped Title", fontsize=18)
                document.save(source)
            engine = LayoutRecordingEngine()
            request = document_parser_pb2.PaperExtractionRequest(
                document_id=str(uuid4()),
                markdown=b"A Scoped Title",
                source_storage_key="pdf/paper.pdf",
                blocks=[document_parser_pb2.PaperSourceBlock(
                    ordinal=3,
                    type="title",
                    text="A Scoped Title",
                    location=_location(0, len("A Scoped Title")),
                )],
            )
            PaperMetadataExtractionRPC(engine, MAX_MARKDOWN_BYTES, MAX_BLOCKS, root).Extract(
                request, FakeContext(),
            )

        self.assertEqual(len(engine.received_layout), 1)
        self.assertEqual(engine.received_layout[0].parent_block_ordinal, 3)


if __name__ == "__main__":
    unittest.main()
