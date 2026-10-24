"""Focused regression tests for bundle integrity and extraction boundaries."""
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

from desktop_bundle import extract, inventory, sha256, verify, write_json


class BundleTests(unittest.TestCase):
    def test_sealed_bundle_detects_content_addition_and_manifest_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "program").write_bytes(b"native executable fixture")
            record = {"schema_version": 1, "distribution": "internal-validation-only", "files": inventory(root)}
            write_json(root / "bundle.json", record)
            checks = f"{sha256(root / 'program')}  program\n{sha256(root / 'bundle.json')}  bundle.json\n"
            (root / "SHA256SUMS").write_text(checks)
            verify(root)
            (root / "unexpected").write_text("developer credential")
            with self.assertRaises(ValueError):
                verify(root)
            (root / "unexpected").unlink()
            (root / "program").write_text("modified")
            with self.assertRaises(ValueError):
                verify(root)

    def test_archive_traversal_does_not_escape_destination(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "unsafe.zip"
            with zipfile.ZipFile(archive, "w") as output:
                output.writestr("../escaped", b"no")
            with self.assertRaises(ValueError):
                extract(archive, root / "zip")
            self.assertFalse((root / "escaped").exists())
            archive = root / "unsafe.tar"
            with tarfile.open(archive, "w") as output:
                item = tarfile.TarInfo("../escaped")
                item.size = 2
                output.addfile(item, io.BytesIO(b"no"))
            with self.assertRaises(tarfile.FilterError):
                extract(archive, root / "tar")
            self.assertFalse((root / "escaped").exists())

    def test_external_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = root / "bundle"
            bundle.mkdir()
            outside = root / "outside"
            outside.write_text("not shipped")
            try:
                (bundle / "link").symlink_to(outside)
            except OSError as error:
                self.skipTest(f"test account cannot create symlinks: {error}")
            with self.assertRaises(ValueError):
                inventory(bundle)


if __name__ == "__main__":
    unittest.main()
