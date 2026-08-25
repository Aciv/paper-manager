"""Focused tests for the paper extraction gRPC transport."""
from __future__ import annotations

import unittest
from uuid import uuid4

import document_parser_pb2

from papers_parser.extractor_service import (
    PaperMetadataExtractionRPC,
    _decode_extraction_request,
)
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine


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
        self.assertEqual(list(response.references), ["[1] Raw reference."])
        self.assertEqual(response.extractor_name, "markdown-rules")

    def test_accepts_request_without_markdown_checksum(self) -> None:
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()),
            markdown=b"# Paper",
        )
        markdown, blocks = _decode_extraction_request(
            request,
            MAX_MARKDOWN_BYTES,
            MAX_BLOCKS,
        )
        self.assertEqual(markdown, "# Paper")
        self.assertEqual(blocks, ())


if __name__ == "__main__":
    unittest.main()
