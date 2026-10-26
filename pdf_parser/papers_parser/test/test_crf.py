"""CRF runtime: hybrid/crf/shadow fallback behavior, native model loading, and bundle validation.

Consolidated parser test module; see the per-test docstrings for the exact contract.
"""

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
import json
import shutil
import tempfile
from unittest.mock import Mock
from pathlib import Path
import pycrfsuite
import os
import subprocess
import sys
from tempfile import TemporaryDirectory
from paper_crf import BundleError, load_bundle

# ---------------------------------------------------------------------------
# Consolidated from test_crf_fallback.py.
# ---------------------------------------------------------------------------

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
        """Verify that the et al. anchor recovers a title word absorbed into the CRF author run."""
        reference = "- [1] Ada et al. Palm 2 technical report. _arXiv preprint arXiv:2303.08774_ , 2023."
        tokens = _citation_tokens(reference)
        # The CRF absorbed "Palm" into the author run after "et al".
        labels = _labels_for(tokens, author_through=6, extra_author=(8,))
        self.assertEqual(_fallback_title(reference, tokens, labels), "Palm 2 technical report")

    def test_last_author_anchor_without_et_al(self) -> None:
        """Verify that the last author token anchors the title when no et al. marker exists."""
        reference = "Meta AI. Llama 3. _CoRR_ , 2024. Accessed: 2024-11-12. 1, 2, 6, 7, 14, 17"
        tokens = _citation_tokens(reference)
        labels = _labels_for(tokens, author_through=1)
        self.assertEqual(_fallback_title(reference, tokens, labels), "Llama 3")

    def test_proceedings_marker(self) -> None:
        """Verify that a proceedings marker terminates the title extracted after the author run."""
        reference = "S. Author and B. Writer. Towards great titles. _Proceedings of the 62nd Meeting_ , 2024."
        tokens = _citation_tokens(reference)
        # AUTHOR run covers "S. Author and B. Writer." (periods are I-AUTHOR).
        labels = _labels_for(tokens, author_through=6)
        self.assertEqual(_fallback_title(reference, tokens, labels), "Towards great titles")

    def test_no_marker_returns_empty(self) -> None:
        """Verify that a reference with no recognized marker yields an empty fallback title."""
        reference = "S. Author. A plain journal entry without any marker. 2024. 2"
        tokens = _citation_tokens(reference)
        labels = _labels_for(tokens, author_through=2)
        self.assertEqual(_fallback_title(reference, tokens, labels), "")

    def test_missing_author_run_returns_empty(self) -> None:
        """Verify that a reference without any AUTHOR labels yields an empty fallback title."""
        reference = "arXiv:2303.08774 , 2023"
        tokens = _citation_tokens(reference)
        self.assertEqual(_fallback_title(reference, tokens, ["O"] * len(tokens)), "")


class SegmentFallbackTitleTest(unittest.TestCase):
    def test_skips_year_stamps_with_letter_suffix(self) -> None:
        """Verify that year stamps with a letter suffix are skipped before the real title."""
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
        """Verify that date and digit-only segments are skipped when selecting the title."""
        reference = "H. Liu, and Y. J. Lee. Visual Instruction Tuning. _Advances in Neural Information Processing Systems_ , 2024. 2, 3"
        tokens = _citation_tokens(reference)
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Lee")
        labels = _labels_for(tokens, author_through=author_through)
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "Visual Instruction Tuning")

    def test_skips_url_and_accessed_segments(self) -> None:
        """Verify that URL and access-date segments are skipped and the preceding phrase becomes the title."""
        reference = "Anthropic. Claude-3.7-sonnet. https://www.anthropic.com/news. Accessed: 2025-03-25"
        tokens = _citation_tokens(reference)
        author_through = next(index for index, token in enumerate(tokens) if token.text == "Anthropic")
        labels = _labels_for(tokens, author_through=author_through)
        # "Claude-3.7-sonnet" precedes the URL and wins.
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "Claude-3.7-sonnet")

    def test_no_author_zone_returns_empty(self) -> None:
        """Verify that an empty author zone produces no segmented fallback title."""
        reference = "Some publisher. 2023. Without labeled authors."
        tokens = _citation_tokens(reference)
        self.assertEqual(_segment_fallback_title(reference, tokens, ["O"] * len(tokens)), "")

    def test_et_al_anchor_keeps_title_word(self) -> None:
        """Verify that segmented fallback keeps the technical title word following an et al. author run."""
        reference = "R. Anil et al. Palm 2 technical report. _arXiv preprint arXiv:2305.10403_ , 2023."
        tokens = _citation_tokens(reference)
        labels = _labels_for(tokens, author_through=3)
        self.assertEqual(_segment_fallback_title(reference, tokens, labels), "Palm 2 technical report")

    def test_skips_url_path_and_version_fragments(self) -> None:
        """Verify that URL path and version fragments are never returned as fallback titles."""
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
        """Verify that an unclosed emphasized venue is split out of the title without a closing marker."""
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
        """Verify that a closed emphasized venue is moved out while keeping the title's question mark."""
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
        """Verify that a closed ArXiv venue is extracted even when followed by an abs identifier."""
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
        """Verify that closed emphasis with unrecognized trailing text stays in the title."""
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
        """Verify that a venue repeated as structured metadata is removed from the title."""
        title, venue = _normalize_reference_title_venue(
            "A robust paper title. _Proceedings of Testing_",
            "Proceedings of Testing",
        )
        self.assertEqual(title, "A robust paper title")
        self.assertEqual(venue, "Proceedings of Testing")

    def test_does_not_split_title_punctuation(self) -> None:
        """Verify that ordinary title punctuation is not mistaken for a venue separator."""
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
        """Verify that a plain second sentence is kept in the title rather than treated as venue."""
        title, venue = _normalize_reference_title_venue(
            "An analysis. Part II",
            "",
        )
        self.assertEqual(title, "An analysis. Part II")
        self.assertEqual(venue, "")

    def test_drops_single_word_field_but_keeps_multiword_fields(self) -> None:
        """Verify that single-word noise fields are dropped while multiword title fields are kept."""
        title, venue = _normalize_reference_title_venue(
            "Primary paper title. Noise. Useful subtitle",
            "",
        )
        self.assertEqual(title, "Primary paper title. Useful subtitle")
        self.assertEqual(venue, "")

    def test_splits_chinese_sentence_punctuation(self) -> None:
        """Verify that Chinese sentence punctuation separates noise from the retained subtitle."""
        title, venue = _normalize_reference_title_venue(
            "主要 标题。冗余。有效 副标题",
            "",
        )
        self.assertEqual(title, "主要 标题。 有效 副标题")
        self.assertEqual(venue, "")

    def test_keeps_single_word_title_when_every_field_would_be_removed(self) -> None:
        """Verify that a single-word title is retained when every other field would be removed."""
        title, venue = _normalize_reference_title_venue("Webscreenshots.", "")
        self.assertEqual(title, "Webscreenshots.")
        self.assertEqual(venue, "")


class ShadowEngineTest(unittest.TestCase):
    def test_returns_rules_output_and_records_only_disagreement_counts(self) -> None:
        """Verify that shadow mode returns rules output and records only content-free disagreement counters."""
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
        """Verify that hybrid populates citation candidates even when no layout lines are supplied."""
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


class HybridConfidenceSelectionTest(unittest.TestCase):
    class _Engine:
        def __init__(self, output):
            self.output = output

        def extract(self, *_args):
            return self.output

        def reference_candidates(self, _references):
            return ()

    def test_high_confidence_crf_replaces_only_weak_rule_title(self) -> None:
        """Verify that a high-confidence CRF title replaces only a weak rule title, not authors or abstract."""
        rules = ExtractionOutput(
            title=TextCandidate("Author block mistaken for title", 0.60),
            authors=(TextCandidate("Software", 0.85),),
            abstract=TextCandidate("Rule abstract", 0.85),
        )
        learned = ExtractionOutput(
            title=TextCandidate("Correct CRF title", 0.94),
            authors=(TextCandidate("Ada Lovelace", 0.93),),
            abstract=TextCandidate("CRF abstract", 0.92),
        )
        engine = HybridPaperExtractionEngine(self._Engine(rules), self._Engine(learned))

        result = engine.extract("ignored", (), (object(),))

        self.assertEqual(result.title.value, "Correct CRF title")
        self.assertEqual(tuple(author.value for author in result.authors), ("Software",))
        self.assertEqual(result.abstract.value, "Rule abstract")

    def test_structural_heading_beats_lower_confidence_crf_title(self) -> None:
        """Verify that a structural heading title is kept over a lower-confidence CRF alternative."""
        rules = ExtractionOutput(title=TextCandidate("Explicit heading title", 1.0))
        learned = ExtractionOutput(title=TextCandidate("Wrong CRF title", 0.99))
        engine = HybridPaperExtractionEngine(self._Engine(rules), self._Engine(learned))

        result = engine.extract("ignored", (), (object(),))

        self.assertEqual(result.title.value, "Explicit heading title")


class ReferenceSourceTest(unittest.TestCase):
    class _Engine:
        def __init__(self, output):
            self.output = output

        def extract(self, *_args):
            return self.output

        def reference_candidates(self, references):
            return tuple(ReferenceCandidate(raw_reference=ref) for ref in references)

    def test_non_empty_crf_references_are_used(self) -> None:
        """Verify that non-empty CRF references replace the rule references."""
        rules = ExtractionOutput(references=("[rule] First.",))
        learned = ExtractionOutput(
            references=("[crf] First.",), references_detected=True
        )
        engine = HybridPaperExtractionEngine(self._Engine(rules), self._Engine(learned))

        result = engine.extract("ignored", (), (object(),))

        self.assertEqual(result.references, ("[crf] First.",))
        self.assertTrue(result.references_detected)

    def test_empty_crf_references_fall_back_to_rules(self) -> None:
        """Verify that empty CRF references fall back to the rule references."""
        rules = ExtractionOutput(
            references=("[rule] First.",), references_detected=True
        )
        learned = ExtractionOutput()
        engine = HybridPaperExtractionEngine(self._Engine(rules), self._Engine(learned))

        result = engine.extract("ignored", (), (object(),))

        self.assertEqual(result.references, ("[rule] First.",))
        self.assertTrue(result.references_detected)



# ---------------------------------------------------------------------------
# Consolidated from test_native_crf_runtime.py.
# ---------------------------------------------------------------------------

class NativeCRFRuntimeTest(unittest.TestCase):
    def test_train_save_reload_and_infer(self):
        """Verify that a trained CRF model can be saved, reopened, and used to predict labels."""
        with tempfile.TemporaryDirectory(prefix="pm-crf-runtime-") as temporary:
            model = Path(temporary) / "probe.crfsuite"
            trainer = pycrfsuite.Trainer(verbose=False)
            for _ in range(4):
                trainer.append([{"token": "paper"}], ["title"])
                trainer.append([{"token": "alice"}], ["author"])
            trainer.set_params({"c1": 0, "c2": .01, "max_iterations": 10})
            trainer.train(str(model))
            tagger = pycrfsuite.Tagger()
            try:
                tagger.open(str(model))
                self.assertEqual(set(tagger.labels()), {"title", "author"})
                self.assertEqual(tagger.tag([{"token": "paper"}]), ["title"])
                self.assertEqual(tagger.tag([{"token": "alice"}]), ["author"])
            finally:
                tagger.close()

    def test_unicode_model_directory_predicts_same_labels(self):
        """Verify that a model under a Unicode, space-containing directory loads and predicts labels."""
        from papers_parser.crf_engine import _CRF

        with tempfile.TemporaryDirectory(prefix="pm-crf-unicode-") as temporary:
            root = Path(temporary)
            source = root / "ascii.crfsuite"
            trainer = pycrfsuite.Trainer(verbose=False)
            for _ in range(4):
                trainer.append([{"token": "paper"}], ["title"])
                trainer.append([{"token": "alice"}], ["author"])
            trainer.set_params({"c1": 0, "c2": .01, "max_iterations": 10})
            trainer.train(str(source))
            target = root / "安装目录 阅读 with spaces" / "model.crfsuite"
            target.parent.mkdir()
            shutil.copy2(source, target)
            manifest = target.with_suffix(".json")
            manifest.write_text(json.dumps({"schema_version": "paper-crf-artifact/v1",
                                            "feature_version": "probe/v1"}), encoding="utf-8")
            model = _CRF(target, "probe/v1", manifest)
            try:
                labels, probabilities = model.predict([{"token": "paper"}])
                self.assertEqual(labels, ["title"])
                self.assertGreater(probabilities[0], 0.5)
            finally:
                model._tagger.close()

    def test_memory_buffer_survives_temporary_allocations(self):
        """Verify that an in-memory model keeps predicting after repeated temporary allocations."""
        from papers_parser.crf_engine import _open_crf_model

        with tempfile.TemporaryDirectory(prefix="pm-crf-memory-") as temporary:
            root = Path(temporary)
            source = root / "ascii.crfsuite"
            trainer = pycrfsuite.Trainer(verbose=False)
            trainer.append([{"word": "paper"}], ["title"])
            trainer.set_params({"max_iterations": 2})
            trainer.train(str(source))
            model = root / "模型.crfsuite"
            shutil.copy2(source, model)
            real = pycrfsuite.Tagger()
            proxy = Mock()
            proxy.open.side_effect = FileNotFoundError("ANSI path failure")
            proxy.open_inmemory.side_effect = real.open_inmemory
            retained = _open_crf_model(proxy, model, platform_name="nt")
            try:
                self.assertIsInstance(retained, bytes)
                for _ in range(50):
                    scratch = bytearray(model.stat().st_size)
                    self.assertEqual(real.tag([{"word": "paper"}]), ["title"])
                    self.assertGreater(len(scratch), 0)
            finally:
                real.close()

    def test_memory_loader_only_applies_to_windows_unicode_paths(self):
        """Verify that the Unicode path memory loader applies only on Windows and still surfaces load errors."""
        from papers_parser.crf_engine import _open_crf_model

        with tempfile.TemporaryDirectory() as temporary:
            model = Path(temporary) / "模型.crfsuite"
            model.write_bytes(b"same immutable model")
            tagger = Mock()
            tagger.open.side_effect = FileNotFoundError("native ANSI fopen")
            retained = _open_crf_model(tagger, model, platform_name="nt")
            self.assertEqual(retained, b"same immutable model")
            tagger.open.assert_not_called()
            tagger.open_inmemory.assert_called_once_with(b"same immutable model")
            with self.assertRaises(FileNotFoundError):
                _open_crf_model(tagger, model, platform_name="posix")
            ascii_model = model.with_name("ascii.crfsuite")
            ascii_model.write_bytes(b"bad")
            with self.assertRaises(FileNotFoundError):
                _open_crf_model(tagger, ascii_model, platform_name="nt")
            # Corrupt models are not converted into successful loads.
            tagger.open_inmemory.side_effect = ValueError("invalid model")
            with self.assertRaises(ValueError):
                _open_crf_model(tagger, model, platform_name="nt")



# ---------------------------------------------------------------------------
# Consolidated from test_paper_crf_bundle.py.
# ---------------------------------------------------------------------------

def _write_minimal_bundle(root: Path, roles: tuple[str, ...]) -> None:
    specs = root / "specs"
    weights = root / "weights"
    specs.mkdir(parents=True)
    weights.mkdir(parents=True)
    models = {}
    for role in roles:
        (weights / f"{role}.crfsuite").write_bytes(b"unused")
        feature_version = (
            "paper-line-crf/v2" if role == "header" else "paper-citation-crf/v1"
        )
        (specs / f"{role}.json").write_text(
            json.dumps({
                "schema_version": "paper-crf-artifact/v1",
                "feature_version": feature_version,
            }),
            encoding="utf-8",
        )
        models[role] = {
            "weights": f"weights/{role}.crfsuite",
            "spec": f"specs/{role}.json",
        }
    (specs / "bundle.json").write_text(
        json.dumps({
            "schema_version": "paper-crf-bundle/v1",
            "models": models,
            "runtime": {
                "header_threshold": 0.912,
                "reference_threshold": 0.946,
                "header_use_layout": False,
                "reference_use_layout": False,
            },
        }),
        encoding="utf-8",
    )


class BundleValidationTest(unittest.TestCase):
    def test_missing_bundle_is_rejected(self) -> None:
        """Verify that a directory without a bundle manifest is rejected."""
        with TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(BundleError, "bundle manifest is missing"):
                load_bundle(temporary)

    def test_reference_role_is_optional_when_disabled(self) -> None:
        """Verify that the reference role is optional when disabled but required by default."""
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            _write_minimal_bundle(root, ("header", "citation"))
            bundle = load_bundle(root, require_reference=False)
            self.assertEqual(set(bundle.models), {"header", "citation"})
            with self.assertRaisesRegex(BundleError, "missing model role 'reference'"):
                load_bundle(root)

    def test_bundle_path_may_not_escape_its_root(self) -> None:
        """Verify that model paths escaping the bundle root are rejected."""
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "specs").mkdir()
            (root / "specs" / "bundle.json").write_text(
                json.dumps({
                    "schema_version": "paper-crf-bundle/v1",
                    "models": {
                        role: {"weights": "../../outside.crfsuite", "spec": "../../outside.json"}
                        for role in ("header", "reference", "citation")
                    },
                    "runtime": {
                        "header_threshold": 0.912,
                        "reference_threshold": 0.946,
                        "header_use_layout": False,
                        "reference_use_layout": False,
                    },
                }),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(BundleError, "escapes the bundle root"):
                load_bundle(root)


class RuntimeImportBoundaryTest(unittest.TestCase):
    def test_crf_engine_does_not_import_model_train(self) -> None:
        """Verify that importing the CRF runtime does not load the model_train workspace."""
        parser_dir = Path(__file__).resolve().parents[2]
        env = os.environ.copy()
        env["PYTHONPATH"] = str(parser_dir) + os.pathsep + env.get("PYTHONPATH", "")
        script = "import sys; import papers_parser.crf_engine; print('model_train' in sys.modules)"
        completed = subprocess.run(
            [sys.executable, "-c", script],
            cwd=parser_dir,
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stdout.strip(), "False", completed.stdout)
