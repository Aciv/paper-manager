"""Focused tests for citation title fallback and venue-boundary cleanup."""
from __future__ import annotations

import unittest

from papers_parser.crf_engine import (
    HybridPaperExtractionEngine,
    ShadowPaperExtractionEngine,
    _citation_tokens,
    _fallback_title,
    _normalize_reference_title_venue,
    _segment_fallback_title,
)
from papers_parser.paper_extractor import (
    DeterministicPaperExtractionEngine,
    ExtractionOutput,
    ReferenceCandidate,
    TextCandidate,
)


def _labels_for(tokens, author_through=None, extra_author=()):
    """All-O labels; AUTHOR through index ``author_through`` and extras."""
    labels = ["O"] * len(tokens)
    if author_through is not None:
        for index in range(author_through + 1):
            labels[index] = "B-AUTHOR" if index == 0 else "I-AUTHOR"
    for index in extra_author:
        labels[index] = "I-AUTHOR"
    return labels


class FallbackTitleTest(unittest.TestCase):
    def test_et_al_anchor_keeps_swallowed_title_word(self) -> None:
        reference = "- [1] Ada et al. Palm 2 technical report. _arXiv preprint arXiv:2303.08774_ , 2023."
        tokens = _citation_tokens(reference)
        # The CRF absorbed "Palm" into the author run after "et al".
        labels = _labels_for(tokens, author_through=6, extra_author=(8,))
        self.assertEqual(_fallback_title(reference, tokens, labels), "Palm 2 technical report")

    def test_last_author_anchor_without_et_al(self) -> None:
        reference = "Meta AI. Llama 3. _CoRR_ , 2024. Accessed: 2024-11-12. 1, 2, 6, 7, 14, 17"
        tokens = _citation_tokens(reference)
        labels = _labels_for(tokens, author_through=1)
        self.assertEqual(_fallback_title(reference, tokens, labels), "Llama 3")

    def test_proceedings_marker(self) -> None:
        reference = "S. Author and B. Writer. Towards great titles. _Proceedings of the 62nd Meeting_ , 2024."
        tokens = _citation_tokens(reference)
        # AUTHOR run covers "S. Author and B. Writer." (periods are I-AUTHOR).
        labels = _labels_for(tokens, author_through=6)
        self.assertEqual(_fallback_title(reference, tokens, labels), "Towards great titles")

    def test_no_marker_returns_empty(self) -> None:
        reference = "S. Author. A plain journal entry without any marker. 2024. 2"
        tokens = _citation_tokens(reference)
        labels = _labels_for(tokens, author_through=2)
        self.assertEqual(_fallback_title(reference, tokens, labels), "")

    def test_missing_author_run_returns_empty(self) -> None:
        reference = "arXiv:2303.08774 , 2023"
        tokens = _citation_tokens(reference)
        self.assertEqual(_fallback_title(reference, tokens, ["O"] * len(tokens)), "")


class SegmentFallbackTitleTest(unittest.TestCase):
    def test_skips_year_stamps_with_letter_suffix(self) -> None:
        # "2022a." must be filtered before "Towards complete icon labeling ...".
        reference = "Jieshan Chen, and Xiaoyi Zhang. 2022a. Towards complete icon labeling in mobile applications. In _CHI_ , pages 387:1."
        tokens = _citation_tokens(reference)
        # Authors run to "Zhang"; everything after is unlabeled.
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Zhang")
        labels = _labels_for(tokens, author_through=author_through)
        self.assertEqual(
            _segment_fallback_title(reference, tokens, labels),
            "Towards complete icon labeling in mobile applications",
        )

    def test_skips_date_and_digit_only_segments(self) -> None:
        reference = "H. Liu, and Y. J. Lee. Visual Instruction Tuning. _Advances in Neural Information Processing Systems_ , 2024. 2, 3"
        tokens = _citation_tokens(reference)
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Lee")
        labels = _labels_for(tokens, author_through=author_through)
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "Visual Instruction Tuning")

    def test_skips_url_and_accessed_segments(self) -> None:
        reference = "Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news. Accessed: 2025-03-25"
        tokens = _citation_tokens(reference)
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Anthropic")
        labels = _labels_for(tokens, author_through=author_through)
        # "Claude-3.7-sonnet" precedes the URL and wins.
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "Claude-3.7-sonnet")

    def test_no_author_zone_returns_empty(self) -> None:
        reference = "Some publisher. 2023. Without labeled authors."
        tokens = _citation_tokens(reference)
        self.assertEqual(_segment_fallback_title(reference, tokens, ["O"] * len(tokens)), "")

    def test_et_al_anchor_keeps_title_word(self) -> None:
        reference = "R. Anil et al. Palm 2 technical report. _arXiv preprint arXiv:2305.10403_ , 2023."
        tokens = _citation_tokens(reference)
        labels = _labels_for(tokens, author_through=3)
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "Palm 2 technical report")

    def test_skips_url_path_and_version_fragments(self) -> None:
        reference = "F. Aydos. Webscreenshots. https://www.kaggle. com/datasets/aydosphd/webscreenshots. Accessed: 2025-03-25"
        tokens = _citation_tokens(reference)
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Webscreenshots")
        labels = _labels_for(tokens, author_through=author_through)
        # URL fragments contain "/" and must not leak in as titles.
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "")
        reference = "DeepMind. Genini-2.5-pro. https://deepmind. google/models/gemini/pro/. Accessed: 2025-0325"
        tokens = _citation_tokens(reference)
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Genini-2")
        labels = _labels_for(tokens, author_through=author_through)
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "")


class ReferenceTitleVenueNormalizationTest(unittest.TestCase):
    def test_moves_unclosed_markdown_venue_out_of_title(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "Deep learning-based prototyping of android GUI from hand-drawn mockups. _IET Softw",
            "",
        )
        self.assertEqual(
            title,
            "Deep learning-based prototyping of android GUI from hand-drawn mockups",
        )
        self.assertEqual(venue, "IET Softw")

    def test_moves_closed_markdown_venue_and_preserves_question_mark(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "Design2code: How far are we from automating front-end engineering ? _CoRR_",
            "",
        )
        self.assertEqual(
            title,
            "Design2code: How far are we from automating front-end engineering?",
        )
        self.assertEqual(venue, "CoRR")

    def test_moves_closed_arxiv_venue_before_abs_identifier(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "Design2Code: How Far Are We From Automating Front-End Engineering ? _ArXiv_ abs / 2403.03163",
            "",
        )
        self.assertEqual(
            title,
            "Design2Code: How Far Are We From Automating Front-End Engineering?",
        )
        self.assertEqual(venue, "ArXiv")

    def test_keeps_closed_emphasis_with_unrecognized_trailing_text(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "A title. _Emphasized subtitle_ unrelated trailing words",
            "",
        )
        self.assertEqual(
            title,
            "A title. _Emphasized subtitle_ unrelated trailing words",
        )
        self.assertEqual(venue, "")

    def test_removes_repeated_structured_venue(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "A robust paper title. _Proceedings of Testing_",
            "Proceedings of Testing",
        )
        self.assertEqual(title, "A robust paper title")
        self.assertEqual(venue, "Proceedings of Testing")

    def test_does_not_split_title_punctuation(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "Design2code: How far are we from automating front-end engineering?",
            "",
        )
        self.assertEqual(
            title,
            "Design2code: How far are we from automating front-end engineering?",
        )
        self.assertEqual(venue, "")

    def test_does_not_treat_plain_second_sentence_as_venue(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "An analysis. Part II",
            "",
        )
        self.assertEqual(title, "An analysis. Part II")
        self.assertEqual(venue, "")

    def test_drops_single_word_field_but_keeps_multiword_fields(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "Primary paper title. Noise. Useful subtitle",
            "",
        )
        self.assertEqual(title, "Primary paper title. Useful subtitle")
        self.assertEqual(venue, "")

    def test_splits_chinese_sentence_punctuation(self) -> None:
        title, venue = _normalize_reference_title_venue(
            "主要 标题。冗余。有效 副标题",
            "",
        )
        self.assertEqual(title, "主要 标题。 有效 副标题")
        self.assertEqual(venue, "")

    def test_keeps_single_word_title_when_every_field_would_be_removed(self) -> None:
        title, venue = _normalize_reference_title_venue("Webscreenshots.", "")
        self.assertEqual(title, "Webscreenshots.")
        self.assertEqual(venue, "")


class ShadowEngineTest(unittest.TestCase):
    def test_returns_rules_output_and_records_only_disagreement_counts(self) -> None:
        rules_output = ExtractionOutput(
            title=TextCandidate("Rule title", 1),
            extractor_name="markdown-rules",
        )
        crf_output = ExtractionOutput(
            title=TextCandidate("CRF title", 1),
            extractor_name="paper-crf",
        )

        class Engine:
            def __init__(self, output):
                self.output = output

            def extract(self, *_args):
                return self.output

        shadow = ShadowPaperExtractionEngine(Engine(rules_output), Engine(crf_output))
        returned = shadow.extract("# ignored", (), (object(),))

        self.assertIs(returned, rules_output)
        self.assertEqual(shadow.safe_counters()["runs"], 1)
        self.assertEqual(shadow.safe_counters()["title_disagreements"], 1)
        self.assertEqual(shadow.safe_counters()["reference_disagreements"], 0)


class HybridNoLayoutTest(unittest.TestCase):
    """Without layout lines hybrid must still enrich citation candidates."""

    class _FakeCitationCRF:
        def reference_candidates(self, references):
            return tuple(ReferenceCandidate(raw_reference=r, title="Rescued Title") for r in references)

    def test_candidates_populated_without_layout(self) -> None:
        markdown = """# A Title

**Alice Zhang**

## Abstract

Abstract text.

## References

[1] First Paper.
[2] Second Paper.
"""
        engine = HybridPaperExtractionEngine(
            DeterministicPaperExtractionEngine(),
            self._FakeCitationCRF(),
        )
        result = engine.extract(markdown, (), ())
        self.assertEqual(result.references, ("[1] First Paper.", "[2] Second Paper."))
        self.assertEqual([c.title for c in result.reference_candidates], ["Rescued Title", "Rescued Title"])
        self.assertEqual(result.extractor_name, "paper-hybrid-no-layout")
        self.assertIsNotNone(result.title)


if __name__ == "__main__":
    unittest.main()
