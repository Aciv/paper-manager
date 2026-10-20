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
        author_start = markdown.index("Ada Lovelace")
        abstract_heading_start = markdown.index("Abstract")
        abstract_start = markdown.index("An explainable abstract.")
        references_heading_start = markdown.index("References")
        reference_start = markdown.index("[1] A raw citation.")
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
            SourceBlock(
                ordinal=4,
                type="text",
                text="### **Ada Lovelace**",
                location=_location(author_start, author_start + len("Ada Lovelace")),
            ),
            SourceBlock(
                ordinal=5,
                type="section_header",
                text="## **Abstract**",
                location=_location(abstract_heading_start, abstract_heading_start + len("Abstract")),
            ),
            SourceBlock(
                ordinal=6,
                type="text",
                text="An explainable abstract.",
                location=_location(abstract_start, abstract_start + len("An explainable abstract.")),
            ),
            SourceBlock(
                ordinal=7,
                type="section_header",
                text="## **References**",
                location=_location(references_heading_start, references_heading_start + len("References")),
            ),
            SourceBlock(
                ordinal=8,
                type="text",
                text="[1] A raw citation.",
                location=_location(reference_start, reference_start + len("[1] A raw citation.")),
            ),
        )

        result = DeterministicPaperExtractionEngine().extract(markdown, blocks)

        self.assertEqual(result.title.value, "Evidence Paper")
        self.assertEqual(result.title.confidence, 1.0)
        self.assertEqual(result.title.evidence[0].block_ordinal, 3)
        self.assertEqual(tuple(author.value for author in result.authors), ("Ada Lovelace",))
        self.assertEqual(result.abstract.value, "An explainable abstract.")
        self.assertTrue(result.references_detected)
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
        self.assertFalse(result.references_detected)
        self.assertEqual(result.references, ())

    def test_prefers_first_page_leading_text_over_later_section_heading(self) -> None:
        title = "- - SCREENCODER: ADVANCING VISUAL TO-CODE GEN - ERATION FOR FRONT-END AUTOMATION VIA MODU LAR MULTIMODAL AGENTS"
        blocks = (
            SourceBlock(ordinal=0, type="text", text=title, location=_location(0, len(title))),
            SourceBlock(ordinal=1, type="text", text="Ada Lovelace", location=_location(len(title) + 1, len(title) + 13)),
            SourceBlock(
                ordinal=10,
                type="section_header",
                text="# 1 INTRODUCTION",
                location=SourceLocation(
                    page_number=2,
                    page_width=612,
                    page_height=792,
                    bbox_x0=72,
                    bbox_y0=72,
                    bbox_x1=300,
                    bbox_y1=110,
                    markdown_codepoint_start=len(title) + 14,
                    markdown_codepoint_end=len(title) + 30,
                ),
            ),
        )

        result = DeterministicPaperExtractionEngine().extract(title, blocks)

        self.assertIsNotNone(result.title)
        self.assertEqual(
            result.title.value,
            "SCREENCODER: ADVANCING VISUAL TO-CODE GENERATION FOR FRONT-END AUTOMATION VIA MODU LAR MULTIMODAL AGENTS",
        )
        self.assertEqual(result.title.evidence[0].block_ordinal, 0)
        self.assertEqual(result.title.confidence, 0.60)

    def test_prefers_obvious_title_heading_over_following_author_text(self) -> None:
        cases = (
            "DeclarUI: Bridging Design and Development with Automated Declarative UI Code Generation",
            "MLLM-Based UI2Code Automation Guided by UI Layout Information",
            "SCREENCODER: ADVANCING VISUAL TO-CODE GENERATION FOR FRONT-END AUTOMATION VIA MODULAR MULTIMODAL AGENTS",
        )
        for title in cases:
            with self.subTest(title=title):
                heading = f"# **{title}**"
                blocks = (
                    SourceBlock(ordinal=0, type="section_header", text=heading, location=_location(0, len(heading))),
                    SourceBlock(
                        ordinal=1,
                        type="text",
                        text="FAN WU, University; CUIYUN GAO, University",
                        location=_location(len(heading) + 1, len(heading) + 48),
                    ),
                )

                result = DeterministicPaperExtractionEngine().extract(heading, blocks)

                self.assertEqual(result.title.value, title)
                self.assertEqual(result.title.evidence[0].block_ordinal, 0)
                self.assertEqual(result.title.confidence, 1.0)

    def test_uses_rule_selected_markdown_range_not_first_matching_text(self) -> None:
        markdown = """# **Repeated**

## Abstract

Repeated
"""
        title_start = markdown.index("Repeated")
        abstract_start = markdown.rindex("Repeated")
        blocks = (
            SourceBlock(
                ordinal=1,
                type="title",
                text="Repeated",
                location=_location(title_start, title_start + len("Repeated")),
            ),
            SourceBlock(
                ordinal=2,
                type="section_header",
                text="## Abstract",
                location=_location(markdown.index("Abstract"), markdown.index("Abstract") + len("Abstract")),
            ),
            SourceBlock(
                ordinal=3,
                type="text",
                text="Repeated",
                location=_location(abstract_start, abstract_start + len("Repeated")),
            ),
        )

        result = DeterministicPaperExtractionEngine().extract(markdown, blocks)

        self.assertEqual(result.title.evidence[0].block_ordinal, 1)
        self.assertEqual(result.abstract.evidence[0].block_ordinal, 3)


def _location(start: int, end: int) -> SourceLocation:
    """Construct a minimal valid source location for range-mapping tests."""
    return SourceLocation(
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


if __name__ == "__main__":
    unittest.main()
