"""Operational regression tests for parser model and logging fallbacks."""
from __future__ import annotations

import logging
import os
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch

import logger
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine
from papers_parser.server import PROJECT_ROOT, _build_extraction_engine, _resolve_project_path


class RuntimeEngineFallbackTest(unittest.TestCase):
    """Missing artifacts must only make explicit CRF mode fail closed."""

    def test_rules_mode_does_not_require_artifacts(self) -> None:
        with TemporaryDirectory() as temporary:
            engine = _build_extraction_engine("rules", Path(temporary))
        self.assertIsInstance(engine, DeterministicPaperExtractionEngine)

    def test_relative_model_directory_is_resolved_from_repository_root(self) -> None:
        self.assertEqual(
            _resolve_project_path("./model_train/artifacts"),
            PROJECT_ROOT / "model_train" / "artifacts",
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
            with self.assertRaisesRegex(ValueError, "artifact is missing"):
                _build_extraction_engine("crf", Path(temporary))

    def test_hybrid_falls_back_but_crf_rejects_corrupt_or_incompatible_manifests(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            header = root / "phase21" / "header-text-title-priors.crfsuite"
            header.parent.mkdir(parents=True)
            header.write_bytes(b"unused because manifest validation runs first")
            manifest = header.with_suffix(".json")
            for contents, message in (("{not-json", "corrupt"), ('{"feature_version":"wrong"}', "mismatch")):
                with self.subTest(kind=message):
                    manifest.write_text(contents, encoding="utf-8")
                    self.assertIsInstance(_build_extraction_engine("hybrid", root), DeterministicPaperExtractionEngine)
                    with self.assertRaises(Exception):
                        _build_extraction_engine("crf", root)


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
