"""A release must reuse a complete set of archives from exact-source tests."""
import json
from pathlib import Path
import tempfile
import unittest

from desktop_bundle import sha256
from prepare_release import release_archives, select_test_run, TARGETS


class ReleaseTests(unittest.TestCase):
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
