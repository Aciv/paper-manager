"""Focused tests for the paper extraction gRPC transport."""
from __future__ import annotations

import hashlib
import unittest
from uuid import uuid4

import document_parser_pb2

from papers_parser.server import (
    PaperMetadataExtractionRPC,
    _decode_extraction_request,
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
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()),
            markdown_sha256=hashlib.sha256(markdown).hexdigest(),
            markdown=markdown,
            blocks=[
                document_parser_pb2.PaperSourceBlock(
                    ordinal=0,
                    type="title",
                    text="RPC Paper",
                    location=document_parser_pb2.PaperSourceLocation(
                        page_number=1,
                        page_width=612,
                        page_height=792,
                        bbox_x0=72,
                        bbox_y0=72,
                        bbox_x1=300,
                        bbox_y1=110,
                        markdown_codepoint_start=title_start,
                        markdown_codepoint_end=title_end,
                    ),
                )
            ],
        )

        response = PaperMetadataExtractionRPC().Extract(request, FakeContext())

        self.assertEqual(response.title.value, "RPC Paper")
        self.assertEqual(response.title.evidence[0].block_ordinal, 0)
        self.assertEqual(response.authors[0].value, "Grace Hopper")
        self.assertEqual(response.abstract.value, "RPC extraction.")
        self.assertEqual(list(response.references), ["[1] Raw reference."])
        self.assertEqual(response.extractor_name, "markdown-rules")

    def test_rejects_stale_markdown_revision(self) -> None:
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()),
            markdown_sha256="0" * 64,
            markdown=b"# Paper",
        )

        with self.assertRaisesRegex(
            ValueError,
            "markdown_sha256 does not match markdown",
        ):
            _decode_extraction_request(request)


if __name__ == "__main__":
    unittest.main()

