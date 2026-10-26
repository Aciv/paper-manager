"""Metadata and reference extraction: rules engine, RPC boundary, Markdown sections, reference filtering.

Consolidated parser test module; see the per-test docstrings for the exact contract.
"""

from __future__ import annotations

import unittest
from papers_parser.paper_extractor import (
    DeterministicPaperExtractionEngine,
    SourceBlock,
    SourceLocation,
)
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
from papers_parser.regex_metadata_find import extract_paper_sections
import time
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine, SourceBlock
from papers_parser.regex_metadata_find import (
    filter_reference_items,
    is_affiliation_only,
    reference_item_ordinal,
    split_reference_items,
)

# ---------------------------------------------------------------------------
# Consolidated from test_paper_extractor.py.
# ---------------------------------------------------------------------------

class DeterministicPaperExtractionEngineTest(unittest.TestCase):
    def test_extracts_candidates_raw_references_and_source_evidence(self) -> None:
        """Verify that rules extraction returns title, authors, abstract, references, and block-ordinal evidence."""
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
        """Verify that non-paper plain text yields no title, authors, abstract, or references."""
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
        """Verify that a first-page leading title line wins over a later first-level section heading."""
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
        """Verify that an obvious bold title heading is chosen over the following author line at full confidence."""
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
        """Verify that evidence ranges follow the rule-selected Markdown span rather than the first text match."""
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



# ---------------------------------------------------------------------------
# Consolidated from test_paper_extraction_rpc.py.
# ---------------------------------------------------------------------------

MAX_MARKDOWN_BYTES = 32 << 20
MAX_BLOCKS = 50_000


def _rpc_location(start: int, end: int):
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
        """Verify that a bounded protobuf extraction request maps to the expected deterministic response fields."""
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
                    location=_rpc_location(title_start, title_end),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=1,
                    type="text",
                    text="### **Grace Hopper**",
                    location=_rpc_location(author_start, author_start + len("Grace Hopper")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=2,
                    type="section_header",
                    text="## Abstract",
                    location=_rpc_location(abstract_heading_start, abstract_heading_start + len("Abstract")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=3,
                    type="text",
                    text="RPC extraction.",
                    location=_rpc_location(abstract_start, abstract_start + len("RPC extraction.")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=4,
                    type="section_header",
                    text="## References",
                    location=_rpc_location(references_heading_start, references_heading_start + len("References")),
                ),
                document_parser_pb2.PaperSourceBlock(
                    ordinal=5,
                    type="text",
                    text="[1] Raw reference.",
                    location=_rpc_location(reference_start, reference_start + len("[1] Raw reference.")),
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
        """Verify that a request lacking a Markdown checksum decodes with empty blocks and storage key."""
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
        """Verify that the optional source storage key survives request decoding."""
        request = document_parser_pb2.PaperExtractionRequest(
            document_id=str(uuid4()), markdown=b"# Paper\n",
            source_storage_key="pdf/source.pdf",
        )
        _, _, source_storage_key = _decode_extraction_request(request, MAX_MARKDOWN_BYTES, MAX_BLOCKS)
        self.assertEqual(source_storage_key, "pdf/source.pdf")

    def test_rejects_oversized_source_storage_key(self) -> None:
        """Verify that a source storage key longer than the 1024-byte bound is rejected."""
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
        """Verify that lazy-layout extraction rejects storage keys escaping the parser upload directory."""
        with TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "outside the parser upload directory"):
                extract_layout_lines_from_storage(
                    Path(directory), "../outside.pdf", (), 10,
                )

    def test_rpc_lazily_supplies_layout_to_layout_aware_engine(self) -> None:
        """Verify that the RPC derives layout lines for layout-aware engines from the stored PDF."""
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
                    location=_rpc_location(0, len("A Scoped Title")),
                )],
            )
            PaperMetadataExtractionRPC(engine, MAX_MARKDOWN_BYTES, MAX_BLOCKS, root).Extract(
                request, FakeContext(),
            )

        self.assertEqual(len(engine.received_layout), 1)
        self.assertEqual(engine.received_layout[0].parent_block_ordinal, 3)



# ---------------------------------------------------------------------------
# Consolidated from test_markdown_metadata.py.
# ---------------------------------------------------------------------------

class MarkdownPaperSectionsTest(unittest.TestCase):
    def assert_reference_texts(self, sections, expected: list[str]) -> None:
        self.assertEqual(sections.references, expected)

    def test_extracts_bold_title_authors_abstract_and_references(self) -> None:
        """Verify that bold title, authors, abstract, and references are parsed while appendices are ignored."""
        markdown = """# **A Paper About Parsing**

### **Alice Zhang, Bob Li**

## **Abstract**

This paper describes a deterministic parser.

## **Introduction**

Body text.

## **References**

[1] Example Paper. https://doi.org/10.1000/example

## **Appendix**

Ignored.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.title, "A Paper About Parsing")
        self.assertEqual(sections.authors, ("Alice Zhang", "Bob Li"))
        self.assertEqual(sections.abstract, "This paper describes a deterministic parser.")
        self.assert_reference_texts(sections, ["[1] Example Paper. https://doi.org/10.1000/example"])

    def test_extracts_inline_bold_authors_and_ignores_superscripts(self) -> None:
        """Verify that inline bold author names are split and superscript affiliation markers are ignored."""
        markdown = """# **Sample Paper**

**Xinhao Huang**<sup>1</sup> **, Jinke Yu**<sup>2</sup> **, Zulong Chen**<sup>2*</sup>
1 HKUST 2 Alibaba Group

## **摘要**

这是摘要内容。

## **参考文献**

[1] 一篇论文。
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.authors, ("Xinhao Huang", "Jinke Yu", "Zulong Chen"))
        self.assertEqual(sections.abstract, "这是摘要内容。")
        self.assert_reference_texts(sections, ["[1] 一篇论文。"])

    def test_uses_conservative_fallback_abstract(self) -> None:
        """Verify that the paragraph before the first numbered section is used as a fallback abstract."""
        markdown = """# **Fallback Abstract**

### **Alice Zhang**

This paragraph is the abstract without an explicit heading.

## **1 Introduction**

Body text.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.abstract, "This paragraph is the abstract without an explicit heading.")

    def test_does_not_treat_affiliation_as_fallback_abstract(self) -> None:
        """Verify that an affiliation and email block is not misread as a fallback abstract."""
        markdown = """# **No Abstract**

### **Alice Zhang**

1 Example University, Example City\nalice@example.edu

## **Introduction**

Body text.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.abstract, "")

    def test_ignores_heading_depth_and_uses_the_first_heading_as_title(self) -> None:
        """Verify that heading depth is ignored and the first heading becomes the title."""
        markdown = """####### **Depth Does Not Matter**

### **Alice Zhang**

#### **Abstract**

The heading depth is parser layout noise.

###### **References**

[1] Example.

### **Appendix**

Ignored.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.title, "Depth Does Not Matter")
        self.assertEqual(sections.authors, ("Alice Zhang",))
        self.assertEqual(sections.abstract, "The heading depth is parser layout noise.")
        self.assert_reference_texts(sections, ["[1] Example."])

    def test_joins_wrapped_numbered_reference_lines(self) -> None:
        """Verify that continuation lines are joined into their numbered reference item."""
        markdown = """# **Reference Items**

## References

[1] First Paper.
    Continued citation details.
[2] Second Paper.
"""

        sections = extract_paper_sections(markdown)

        self.assert_reference_texts(
            sections,
            ["[1] First Paper. Continued citation details.", "[2] Second Paper."],
        )



# ---------------------------------------------------------------------------
# Consolidated from test_reference_item_filter.py.
# ---------------------------------------------------------------------------

class ReferenceItemOrdinalTest(unittest.TestCase):
    def test_ordinal_styles(self) -> None:
        """Verify that bracket, bullet, dotted, parenthesized, and Chinese list ordinals are recognized."""
        cases = {
            "[1] First paper.": "1",
            "  [2] Indented.": "2",
            "- [3] Bulleted bracket.": "3",
            "• [4] Dot bullet.": "4",
            "3. Dotted number.": "3",
            "4) Parenthesized.": "4",
            "5、Chinese list.": "5",
            "  -  [6] Deep bullet spacing.": "6",
        }
        for text, expected in cases.items():
            with self.subTest(text=text):
                self.assertEqual(reference_item_ordinal(text), expected)

    def test_missing_ordinal_is_invalid(self) -> None:
        """Verify that blank, bullet-only, continuation, and APA-style entries have no ordinal."""
        invalid = [
            "",
            "   ",
            "Author only.",
            "- Bullet without a number.",
            "Continued detail of the previous item.",
            "See [7] and [8].Doe, J. (2020). An APA-style entry.",
        ]
        for text in invalid:
            with self.subTest(text=text):
                self.assertIsNone(reference_item_ordinal(text))


class NumberedRegionTest(unittest.TestCase):
    def test_keeps_first_occurrence_of_each_ordinal(self) -> None:
        """Verify that duplicate ordinals collapse to their first occurrence while keeping order."""
        items = [
            "[1] Alpha.",
            "[2] Beta.",
            "[1] Alpha duplicated.",
            "[3] Gamma.",
            "[2] Beta duplicated.",
        ]
        self.assertEqual(
            filter_reference_items(items),
            ["[1] Alpha.", "[2] Beta.", "[3] Gamma."],
        )

    def test_drops_unnumbered_strays_inside_numbered_region(self) -> None:
        """Verify that unnumbered stray lines inside a numbered region are dropped."""
        items = ["[1] Alpha.", "- Stray continuation.", "[2] Beta.", "No number."]
        self.assertEqual(
            filter_reference_items(items),
            ["[1] Alpha.", "[2] Beta."],
        )

    def test_mixed_ordinal_styles_deduplicate(self) -> None:
        """Verify that the same ordinal expressed in different styles deduplicates to one item."""
        items = ["[1] Bracket style.", "1. Dotted duplicate.", "[2] Second."]
        self.assertEqual(filter_reference_items(items), ["[1] Bracket style.", "[2] Second."])

    def test_cross_block_duplicates_are_deduplicated_once(self) -> None:
        """Verify that duplicate items merged from two split blocks are deduplicated once."""
        # One region split across two blocks: [1]..[3] then [1]..[3] again.
        block_one = """[1] First Paper.
    Continued citation details.
[2] Second Paper.
"""
        block_two = """[1] First Paper.
[2] Second Paper.
[3] Third Paper.
"""
        merged = split_reference_items(block_one) + split_reference_items(block_two)
        self.assertEqual(
            filter_reference_items(merged),
            [
                "[1] First Paper. Continued citation details.",
                "[2] Second Paper.",
                "[3] Third Paper.",
            ],
        )


class UnnumberedRegionTest(unittest.TestCase):
    def test_keeps_distinct_bullet_items(self) -> None:
        """Verify that distinct bullet citations are all preserved."""
        items = [
            "- Abdelaziz A. Abdelhamid and Sultan R. Alotaibi. 2020. Deep learning-",
            "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet",
            "- R. Archana and P. S. Eliahim Jeevaraj. 2024. Digital image processing.",
        ]
        self.assertEqual(filter_reference_items(items), items)

    def test_drops_numbered_tail_after_unnumbered_region(self) -> None:
        """Verify that numbered body-text prompts after a bullet bibliography are not treated as references."""
        # Body-text prompts after a bullet bibliography must not become refs.
        items = [
            "- Abdelaziz A. Abdelhamid and Sultan R. Alotaibi. 2020. Deep learning-",
            "1. Visual similarity to the original",
            "2. Layout accuracy",
        ]
        self.assertEqual(filter_reference_items(items), items[:1])

    def test_drops_shorter_near_duplicate_of_kept_item(self) -> None:
        """Verify that a shorter near-duplicate bullet is dropped in favor of the longer kept item."""
        long_item = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet. Accessed: 2025-05-25"
        short_dup = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet."
        self.assertEqual(
            filter_reference_items([long_item, short_dup]),
            [long_item],
        )

    def test_later_longer_duplicate_replaces_shorter_kept_item(self) -> None:
        """Verify that a later longer duplicate replaces the shorter item already kept."""
        short_first = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet."
        long_later = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet. Accessed: 2025-05-25"
        self.assertEqual(
            filter_reference_items([short_first, long_later]),
            [long_later],
        )

    def test_skips_blank_items_before_style_anchor(self) -> None:
        """Verify that blank entries before the first real bullet do not affect filtering."""
        items = ["", "   ", "- First real bullet.", "- Second real bullet."]
        self.assertEqual(
            filter_reference_items(items),
            ["- First real bullet.", "- Second real bullet."],
        )

    def test_keeps_unrelated_long_citations(self) -> None:
        """Verify that distinct long citations sharing many letters are not treated as duplicates."""
        # Distinct English citations share almost all letters; the overlap
        # rule must be contiguous-match based, not letter-frequency based.
        items = [
            "- Kevin Moran, Carlos Bernal-Cardenas, Michael Curcio, Richard Bonett, Denys Poshyvanyk. 2015. Machine learning-based prototyping of graphical user interfaces.",
            "- Kevin Moran, Boyang Li, Carlos Bernal-Cardenas, Dan Jelf, Denys Poshyvanyk. 2018. Automated reporting of GUI design flaws.",
            "- Xinyi Hou, Yanjie Zhao, Yue Liu, Zhou Yang, Kailong Wang, Li Li, Xiapu Luo, Dongwen Zhang, Haoyu Wang. 2024. Large language models for software engineering.",
        ]
        self.assertEqual(filter_reference_items(items), items)

    def test_keeps_short_distinct_bullets(self) -> None:
        """Verify that short but distinct bullets are all retained."""
        items = [
            "- Image 1: The original web UI design reference",
            "- Image 2: Method A generated UI",
            "- Image 3: Method B generated UI",
        ]
        self.assertEqual(filter_reference_items(items), items)


class LinearTimeRegressionTest(unittest.TestCase):
    """Hostile prefixes and long lines must stay linear (P1-7)."""

    def test_marker_prefix_does_not_backtrack(self) -> None:
        """Verify that hostile repeated bullet prefixes are processed without catastrophic backtracking."""
        hostile = "- " * 5000 + "x"
        started = time.monotonic()
        self.assertIsNone(reference_item_ordinal(hostile))
        self.assertEqual(split_reference_items(hostile), [hostile])
        self.assertEqual(filter_reference_items([hostile]), [hostile])
        self.assertLess(time.monotonic() - started, 1.0)

    def test_email_scan_is_linear_on_long_affiliation_line(self) -> None:
        """Verify that affiliation detection on a very long line completes in linear time."""
        hostile = "department " + ("a" * 50 + " ") * 4000 + "no email here"
        started = time.monotonic()
        self.assertTrue(is_affiliation_only(hostile))
        self.assertLess(time.monotonic() - started, 1.0)

    def test_reference_count_and_length_are_bounded(self) -> None:
        """Verify that reference count and per-item length are capped at the configured bounds."""
        numbered = "\n".join(f"[{index}] Reference {index}." for index in range(5100))
        items = split_reference_items(numbered)
        self.assertEqual(len(items), 5000)

        long_item = "[1] " + "x" * 70000
        bounded = split_reference_items(long_item)
        self.assertEqual(len(bounded), 1)
        self.assertEqual(len(bounded[0]), 65536)


class RuleEngineReferenceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.engine = DeterministicPaperExtractionEngine()

    def test_markdown_fallback_deduplicates_and_rejects_unnumbered(self) -> None:
        """Verify that Markdown fallback deduplicates numbered references and rejects unnumbered fragments."""
        markdown = """# A Title

**Alice Zhang**

## Abstract

Some abstract text.

## References

[1] First Paper.
[2] Second Paper.
[1] First Paper duplicated.
- Unnumbered fragment.
[3] Third Paper.
"""
        result = self.engine.extract(markdown, ())
        self.assertEqual(
            result.references,
            ("[1] First Paper.", "[2] Second Paper.", "[3] Third Paper."),
        )
        self.assertEqual(
            [candidate.raw_reference for candidate in result.reference_candidates],
            list(result.references),
        )

    def test_blockquote_footnote_style_keeps_all_numbered_items(self) -> None:
        """Verify that blockquote footnote markers do not switch the region to unnumbered filtering."""
        # Real PDF layout: pymupdf4llm renders the first references as
        # footnote blocks "> [1] ..." and the rest as "- [4] ..." list items.
        # The ">" marker must not flip the region into unnumbered style and
        # drop every numbered item after it.
        blocks = (
            SourceBlock(0, "title", "A Paper"),
            SourceBlock(1, "section_header", "## Abstract"),
            SourceBlock(2, "text", "Some abstract text."),
            SourceBlock(3, "section_header", "## References"),
            SourceBlock(4, "footnote", "> [1] First Paper. https://example.com"),
            SourceBlock(5, "footnote", "> [2] Second Paper."),
            SourceBlock(6, "footnote", "> [3] Third Paper."),
            SourceBlock(7, "list_item", "- [4] Fourth Paper."),
            SourceBlock(8, "list_item", "- [5] Fifth Paper."),
            SourceBlock(9, "text", "FSE094:22 running page header"),
            SourceBlock(10, "list_item", "- [6] Sixth Paper."),
            SourceBlock(11, "text", "Received 2024-09-13; accepted 2025-04-01"),
        )
        result = self.engine.extract("", blocks)
        self.assertEqual(
            result.references,
            (
                "[1] First Paper. https://example.com",
                "[2] Second Paper.",
                "[3] Third Paper.",
                "- [4] Fourth Paper.",
                "- [5] Fifth Paper.",
                "- [6] Sixth Paper.",
            ),
        )

    def test_markdown_fallback_keeps_unnumbered_bullet_bibliography(self) -> None:
        """Verify that Markdown fallback retains bullet bibliography entries and drops trailing numbered prompts."""
        markdown = """# A Title

**Alice Zhang**

## Abstract

Some abstract text.

## References

- Abdelaziz A. Abdelhamid. 2020. Deep learning models.
- Anthropic. Claude-3.7-sonnet. https://example.com/news

1. Visual similarity to the original
2. Layout accuracy
"""
        result = self.engine.extract(markdown, ())
        self.assertEqual(
            result.references,
            (
                "- Abdelaziz A. Abdelhamid. 2020. Deep learning models.",
                "- Anthropic. Claude-3.7-sonnet. https://example.com/news",
            ),
        )
