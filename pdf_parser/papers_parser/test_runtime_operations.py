"""Operational regression tests for parser model and logging fallbacks."""
from __future__ import annotations

import json
import logging
import os
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch

import logger
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine, LayoutLine
from papers_parser.server import PROJECT_ROOT, _build_extraction_engine, _resolve_project_path

BUNDLE_ROOT = PROJECT_ROOT / "pdf_parser" / "paper_crf"
BUNDLE_WEIGHTS = BUNDLE_ROOT / "weights" / "reference-text.crfsuite"
BUNDLE_HEADER_WEIGHTS = BUNDLE_ROOT / "weights" / "header-text-title-priors.crfsuite"
BUNDLE_CITATION_WEIGHTS = BUNDLE_ROOT / "weights" / "citation-context.crfsuite"
BUNDLE_HEADER_AND_CITATION = BUNDLE_HEADER_WEIGHTS.is_file() and BUNDLE_CITATION_WEIGHTS.is_file()


def _write_bundle(root: Path) -> Path:
    """Create a minimal bundle layout and return the header manifest path."""
    weights = root / "weights"
    specs = root / "specs"
    weights.mkdir(parents=True)
    specs.mkdir(parents=True)
    for name in ("header", "reference", "citation"):
        (weights / f"{name}.crfsuite").write_bytes(b"unused; manifest validation runs first")
    for name, feature_version in (
        ("reference", "paper-line-crf/v2"),
        ("citation", "paper-citation-crf/v1"),
    ):
        (specs / f"{name}.json").write_text(
            json.dumps({
                "schema_version": "paper-crf-artifact/v1",
                "feature_version": feature_version,
            }),
            encoding="utf-8",
        )
    header = specs / "header.json"
    (specs / "bundle.json").write_text(
        json.dumps({
            "schema_version": "paper-crf-bundle/v1",
            "models": {
                "header": {"weights": "weights/header.crfsuite", "spec": "specs/header.json"},
                "reference": {"weights": "weights/reference.crfsuite", "spec": "specs/reference.json"},
                "citation": {"weights": "weights/citation.crfsuite", "spec": "specs/citation.json"},
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
    return header


class RuntimeEngineFallbackTest(unittest.TestCase):
    """Missing artifacts must only make explicit CRF mode fail closed."""

    def test_rules_mode_does_not_require_artifacts(self) -> None:
        with TemporaryDirectory() as temporary:
            engine = _build_extraction_engine("rules", Path(temporary))
        self.assertIsInstance(engine, DeterministicPaperExtractionEngine)

    def test_relative_model_directory_is_resolved_from_repository_root(self) -> None:
        self.assertEqual(
            _resolve_project_path("./pdf_parser/paper_crf"),
            BUNDLE_ROOT,
        )

    def test_relative_upload_directory_is_resolved_from_repository_root(self) -> None:
        self.assertEqual(
            _resolve_project_path("./data/uploads"),
            PROJECT_ROOT / "data" / "uploads",
        )

    def test_hybrid_and_shadow_fall_back_to_rules_when_artifacts_are_missing(self) -> None:
        with TemporaryDirectory() as temporary:
            for mode in ("hybrid", "shadow"):
                with self.subTest(mode=mode):
                    engine = _build_extraction_engine(mode, Path(temporary))
                    self.assertIsInstance(engine, DeterministicPaperExtractionEngine)

    def test_explicit_crf_mode_rejects_missing_artifacts(self) -> None:
        with TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "bundle manifest is missing"):
                _build_extraction_engine("crf", Path(temporary))

    def test_hybrid_falls_back_but_crf_rejects_corrupt_or_incompatible_manifests(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            manifest = _write_bundle(root)
            for contents, message in (
                ("{not-json", "corrupt"),
                ('{"schema_version":"paper-crf-artifact/v1","feature_version":"wrong"}', "mismatch"),
            ):
                with self.subTest(kind=message):
                    manifest.write_text(contents, encoding="utf-8")
                    self.assertIsInstance(
                        _build_extraction_engine("hybrid", root),
                        DeterministicPaperExtractionEngine,
                    )
                    with self.assertRaises(Exception):
                        _build_extraction_engine("crf", root)


class PackagedBundleTest(unittest.TestCase):
    """The checked-out release bundle must load through the same runtime path."""

    @unittest.skipUnless(BUNDLE_WEIGHTS.is_file(), "packaged CRF weights are not present")
    def test_packaged_bundle_loads_and_builds_crf_engine(self) -> None:
        from paper_crf import load_bundle

        bundle = load_bundle(BUNDLE_ROOT)
        self.assertEqual(set(bundle.models), {"header", "reference", "citation"})
        for selection in bundle.models.values():
            self.assertTrue(selection.weights.is_file())
            self.assertTrue(selection.spec.is_file())
        engine = _build_extraction_engine("crf", BUNDLE_ROOT)
        self.assertNotIsInstance(engine, DeterministicPaperExtractionEngine)

    @unittest.skipUnless(BUNDLE_HEADER_AND_CITATION, "packaged CRF weights are not present")
    def test_reference_crf_is_disabled_by_default(self) -> None:
        engine = _build_extraction_engine("hybrid", BUNDLE_ROOT)
        self.assertFalse(engine._crf.reference_enabled)
        self.assertNotIn("reference_model", engine._crf.model_summary())

    @unittest.skipUnless(
        BUNDLE_HEADER_AND_CITATION and BUNDLE_WEIGHTS.is_file(),
        "packaged CRF weights are not present",
    )
    def test_reference_crf_can_be_enabled(self) -> None:
        engine = _build_extraction_engine(
            "hybrid", BUNDLE_ROOT, reference_crf_enabled=True
        )
        self.assertTrue(engine._crf.reference_enabled)
        self.assertIn("reference_model", engine._crf.model_summary())

    @unittest.skipUnless(BUNDLE_HEADER_AND_CITATION, "packaged CRF weights are not present")
    def test_hybrid_uses_rule_references_by_default(self) -> None:
        markdown = (
            "# A Paper\n\n## References\n\n"
            "[1] A. Smith. A study of things. Journal, 2020.\n"
            "[2] B. Jones. Another study. Journal, 2021.\n"
        )
        lines = [
            LayoutLine(
                page_number=1, order_in_page=index, order_in_document=index,
                text=text, page_width=600.0, page_height=800.0,
                bbox_x0=50.0, bbox_y0=50.0 + index * 10,
                bbox_x1=550.0, bbox_y1=60.0 + index * 10,
            )
            for index, text in enumerate((
                "A Paper", "References",
                "[1] A. Smith. A study of things. Journal, 2020.",
                "[2] B. Jones. Another study. Journal, 2021.",
            ))
        ]
        rules = DeterministicPaperExtractionEngine().extract(markdown, (), lines)
        hybrid = _build_extraction_engine("hybrid", BUNDLE_ROOT).extract(markdown, (), lines)
        self.assertEqual(hybrid.references, rules.references)
        self.assertEqual(len(hybrid.reference_candidates), len(rules.references))


class ParserLoggerFallbackTest(unittest.TestCase):
    """A read-only or invalid file target must degrade to container stdout."""

    def test_file_handler_falls_back_when_log_parent_is_a_file(self) -> None:
        with TemporaryDirectory() as temporary:
            blocking_parent = Path(temporary) / "not-a-directory"
            blocking_parent.write_text("blocked", encoding="utf-8")
            with patch.dict(os.environ, {"LOG_PATH": str(blocking_parent / "today.log")}, clear=False):
                handler = logger._build_handler(logging.INFO)
        try:
            self.assertNotIsInstance(handler, logging.FileHandler)
            self.assertIsInstance(handler, logging.StreamHandler)
        finally:
            handler.close()
