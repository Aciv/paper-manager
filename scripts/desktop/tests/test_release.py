"""A release must reuse a complete set of archives from exact-source tests."""
import json
from pathlib import Path
import tempfile
import unittest

from desktop_bundle import sha256
from prepare_release import release_archives, release_notes, select_test_run, TARGETS
from check_desktop_upgrade import upgrade_versions


class ReleaseTests(unittest.TestCase):
    def test_release_notes_require_the_requested_version(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "1.0.1.md").write_text("# paper_manager 1.0.1\n\n更新内容。\n", encoding="utf-8")
            self.assertIn("更新内容", release_notes(root, "1.0.1"))
            with self.assertRaises(FileNotFoundError):
                release_notes(root, "1.0.0")
            with self.assertRaises(ValueError):
                release_notes(root, "../1.0.1")
            (root / "1.0.1.md").write_text("# Wrong version\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                release_notes(root, "1.0.1")

    def test_current_product_has_matching_release_notes(self):
        root = Path(__file__).resolve().parents[3]
        product = json.loads((root / "packaging/desktop/product.json").read_text())
        self.assertTrue(release_notes(root / "packaging/desktop/releases", product["version"]))

    def test_upgrade_fixture_accepts_future_patch_versions(self):
        self.assertEqual(upgrade_versions("1.0.0"), ("1.0.1", "1.0.2"))
        self.assertEqual(upgrade_versions("1.0.1"), ("1.0.2", "1.0.3"))
        with self.assertRaises(ValueError):
            upgrade_versions("invalid")

    def test_selection_rejects_old_failed_and_unfinished_runs(self):
        def run(identifier, **changes):
            return {"id": identifier, "head_sha": "new", "status": "completed",
                    "conclusion": "success", "path": ".github/workflows/test.yml", **changes}
        runs = [run(1), run(2, head_sha="old"), run(3, conclusion="failure"),
                run(4, status="in_progress"), run(5, path="another.yml")]
        self.assertEqual(select_test_run(runs, "new"), 1)
        with self.assertRaises(ValueError):
            select_test_run(runs[1:], "new")

    def test_release_requires_all_targets_and_exact_revision(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for target in sorted(TARGETS):
                suffix = "zip" if target.startswith("windows") else "tar.gz"
                archive = root / f"paper_manager-1.0.0-{target}.{suffix}"
                archive.write_bytes(target.encode())
                archive.with_name(archive.name + ".sha256").write_text(
                    f"{sha256(archive)}  {archive.name}\n")
                manifest = archive.with_name(archive.name + ".manifest.json")
                manifest.write_text(json.dumps({"distribution": "user", "target": target,
                                               "revision": "new", "application": {"version": "1.0.0"}}))
            self.assertEqual(len(release_archives(root, "new", "1.0.0")), 3)
            with self.assertRaisesRegex(ValueError, "stale"):
                release_archives(root, "old", "1.0.0")
            manifest.unlink()
            with self.assertRaisesRegex(ValueError, "all three"):
                release_archives(root, "new", "1.0.0")
