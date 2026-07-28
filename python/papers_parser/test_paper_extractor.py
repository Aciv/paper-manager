"""Tests for the transport-neutral paper extraction engine."""
from __future__ import annotations

import unittest

from papers_parser.paper_extractor import (
    DeterministicPaperExtractionEngine,
    SourceBlock,
    SourceLocation,
)


class DeterministicPaperExtractionEngineTest(unittest.TestCase):
    def test_extracts_candidates_raw_references_and_source_evidence(self) -> None:
        markdown = """# **Evidence Paper**

### **Ada Lovelace**

## **Abstract**

An explainable abstract.

## **References**

[1] A raw citation.
"""
        title_start = markdown.index("Evidence Paper")
        title_end = title_start + len("Evidence Paper")
        blocks = (
            SourceBlock(
                ordinal=3,
                type="title",
                text="Evidence Paper",
                location=SourceLocation(
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
            ),
        )

        result = DeterministicPaperExtractionEngine().extract(markdown, blocks)

        self.assertEqual(result.title.value, "Evidence Paper")
        self.assertEqual(result.title.confidence, 0.95)
        self.assertEqual(result.title.evidence[0].block_ordinal, 3)
        self.assertEqual(tuple(author.value for author in result.authors), ("Ada Lovelace",))
        self.assertEqual(result.abstract.value, "An explainable abstract.")
        self.assertEqual(result.references, ("[1] A raw citation.",))
        self.assertEqual(result.extractor_name, "markdown-rules")

    def test_returns_empty_candidates_for_non_paper_markdown(self) -> None:
        result = DeterministicPaperExtractionEngine().extract(
            "plain text without a heading",
            (),
        )

        self.assertIsNone(result.title)
        self.assertEqual(result.authors, ())
        self.assertIsNone(result.abstract)
        self.assertEqual(result.references, ())


if __name__ == "__main__":
    unittest.main()

