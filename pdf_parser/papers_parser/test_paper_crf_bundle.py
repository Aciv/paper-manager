"""Release-boundary tests for the runtime-owned paper CRF bundle."""
from __future__ import annotations

import json
import os
import subprocess
import sys
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory

from paper_crf import BundleError, load_bundle


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
        with TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(BundleError, "bundle manifest is missing"):
                load_bundle(temporary)

    def test_reference_role_is_optional_when_disabled(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            _write_minimal_bundle(root, ("header", "citation"))
            bundle = load_bundle(root, require_reference=False)
            self.assertEqual(set(bundle.models), {"header", "citation"})
            with self.assertRaisesRegex(BundleError, "missing model role 'reference'"):
                load_bundle(root)

    def test_bundle_path_may_not_escape_its_root(self) -> None:
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
        parser_dir = Path(__file__).resolve().parents[1]
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
