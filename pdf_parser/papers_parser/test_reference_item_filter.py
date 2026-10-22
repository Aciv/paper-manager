"""Focused tests for 序号/overlap-based reference item consolidation."""
from __future__ import annotations

import time
import unittest

from papers_parser.paper_extractor import DeterministicPaperExtractionEngine, SourceBlock
from papers_parser.regex_metadata_find import (
    filter_reference_items,
    is_affiliation_only,
    reference_item_ordinal,
    split_reference_items,
)


class ReferenceItemOrdinalTest(unittest.TestCase):
    def test_ordinal_styles(self) -> None:
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
        items = ["[1] Alpha.", "- Stray continuation.", "[2] Beta.", "No number."]
        self.assertEqual(
            filter_reference_items(items),
            ["[1] Alpha.", "[2] Beta."],
        )

    def test_mixed_ordinal_styles_deduplicate(self) -> None:
        items = ["[1] Bracket style.", "1. Dotted duplicate.", "[2] Second."]
        self.assertEqual(filter_reference_items(items), ["[1] Bracket style.", "[2] Second."])

    def test_cross_block_duplicates_are_deduplicated_once(self) -> None:
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
        items = [
            "- Abdelaziz A. Abdelhamid and Sultan R. Alotaibi. 2020. Deep learning-",
            "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet",
            "- R. Archana and P. S. Eliahim Jeevaraj. 2024. Digital image processing.",
        ]
        self.assertEqual(filter_reference_items(items), items)

    def test_drops_numbered_tail_after_unnumbered_region(self) -> None:
        # Body-text prompts after a bullet bibliography must not become refs.
        items = [
            "- Abdelaziz A. Abdelhamid and Sultan R. Alotaibi. 2020. Deep learning-",
            "1. Visual similarity to the original",
            "2. Layout accuracy",
        ]
        self.assertEqual(filter_reference_items(items), items[:1])

    def test_drops_shorter_near_duplicate_of_kept_item(self) -> None:
        long_item = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet. Accessed: 2025-05-25"
        short_dup = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet."
        self.assertEqual(
            filter_reference_items([long_item, short_dup]),
            [long_item],
        )

    def test_later_longer_duplicate_replaces_shorter_kept_item(self) -> None:
        short_first = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet."
        long_later = "- Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news/claude-3-7-sonnet. Accessed: 2025-05-25"
        self.assertEqual(
            filter_reference_items([short_first, long_later]),
            [long_later],
        )

    def test_skips_blank_items_before_style_anchor(self) -> None:
        items = ["", "   ", "- First real bullet.", "- Second real bullet."]
        self.assertEqual(
            filter_reference_items(items),
            ["- First real bullet.", "- Second real bullet."],
        )

    def test_keeps_unrelated_long_citations(self) -> None:
        # Distinct English citations share almost all letters; the overlap
        # rule must be contiguous-match based, not letter-frequency based.
        items = [
            "- Kevin Moran, Carlos Bernal-Cardenas, Michael Curcio, Richard Bonett, Denys Poshyvanyk. 2015. Machine learning-based prototyping of graphical user interfaces.",
            "- Kevin Moran, Boyang Li, Carlos Bernal-Cardenas, Dan Jelf, Denys Poshyvanyk. 2018. Automated reporting of GUI design flaws.",
            "- Xinyi Hou, Yanjie Zhao, Yue Liu, Zhou Yang, Kailong Wang, Li Li, Xiapu Luo, Dongwen Zhang, Haoyu Wang. 2024. Large language models for software engineering.",
        ]
        self.assertEqual(filter_reference_items(items), items)

    def test_keeps_short_distinct_bullets(self) -> None:
        items = [
            "- Image 1: The original web UI design reference",
            "- Image 2: Method A generated UI",
            "- Image 3: Method B generated UI",
        ]
        self.assertEqual(filter_reference_items(items), items)


class LinearTimeRegressionTest(unittest.TestCase):
    """Hostile prefixes and long lines must stay linear (P1-7)."""

    def test_marker_prefix_does_not_backtrack(self) -> None:
        hostile = "- " * 5000 + "x"
        started = time.monotonic()
        self.assertIsNone(reference_item_ordinal(hostile))
        self.assertEqual(split_reference_items(hostile), [hostile])
        self.assertEqual(filter_reference_items([hostile]), [hostile])
        self.assertLess(time.monotonic() - started, 1.0)

    def test_email_scan_is_linear_on_long_affiliation_line(self) -> None:
        hostile = "department " + ("a" * 50 + " ") * 4000 + "no email here"
        started = time.monotonic()
        self.assertTrue(is_affiliation_only(hostile))
        self.assertLess(time.monotonic() - started, 1.0)

    def test_reference_count_and_length_are_bounded(self) -> None:
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


if __name__ == "__main__":
    unittest.main()
