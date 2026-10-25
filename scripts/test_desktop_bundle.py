"""Focused regression tests for bundle integrity and extraction boundaries."""
import io
import json
import os
import subprocess
import sys
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

    def test_windows_database_extraction_skips_unused_embedded_applications(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "postgres.zip"
            with zipfile.ZipFile(archive, "w") as output:
                output.writestr("pgsql/bin/postgres.exe", b"runtime")
                output.writestr("pgsql/pgAdmin 4/python/unused.dll", b"not needed")
                output.writestr("pgsql/server_license.txt", b"notice")
            destination = root / "extracted"
            extract(archive, destination, prefixes=("pgsql/bin/", "pgsql/server_license.txt"))
            self.assertEqual((destination / "pgsql/bin/postgres.exe").read_bytes(), b"runtime")
            self.assertTrue((destination / "pgsql/server_license.txt").is_file())
            self.assertFalse((destination / "pgsql/pgAdmin 4").exists())

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


class DeliveryTests(unittest.TestCase):
    def bundle(self, root):
        bundle = root / "bundle"
        bundle.mkdir()
        (bundle / "program").write_bytes(b"isolated runtime fixture")
        record = {"schema_version": 1, "distribution": "internal-validation-only", "target": "fixture",
                  "application": {"id": "papers-manager", "display_name": "paper_manager", "version": "1.0.0"},
                  "revision": "test-revision", "profile": "hybrid", "files": inventory(bundle)}
        write_json(bundle / "bundle.json", record)
        checks = f"{sha256(bundle / 'program')}  program\n{sha256(bundle / 'bundle.json')}  bundle.json\n"
        (bundle / "SHA256SUMS").write_text(checks, encoding="utf-8")
        return bundle

    def test_exported_delivery_detects_modified_harness_and_unknown_files(self):
        from package_desktop_bundle import package
        from check_desktop_package import verify_checksum, verify_delivery

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = self.bundle(root)
            archive = package(bundle, root / "delivery.tar.gz")
            verify_checksum(archive)
            self.assertEqual(
                archive.with_name(archive.name + ".sha256").read_bytes(),
                f"{sha256(archive)}  {archive.name}\n".encode("utf-8"),
            )
            destination = root / "extracted 阅读"
            extract(archive, destination)
            verify_delivery(destination)
            package_record = json.loads((destination / "package.json").read_text(encoding="utf-8"))
            self.assertEqual(package_record["application"]["version"], "1.0.0")
            changed = json.loads(json.dumps(package_record))
            changed["application"]["version"] = "1.0.1"
            write_json(destination / "package.json", changed)
            with self.assertRaises(ValueError):
                verify_delivery(destination)
            write_json(destination / "package.json", package_record)
            # Exercise the real CLI with ordinary Python defaults. Importing
            # a sibling must not create an unsealed __pycache__ directory.
            environment = {key: value for key, value in os.environ.items() if key != "PYTHONDONTWRITEBYTECODE"}
            result = subprocess.run([
                sys.executable, str(destination / "validation/check_desktop_package.py"),
                "--directory", str(destination), "--verify-only",
            ], env=environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse((destination / "validation/__pycache__").exists())
            verify_delivery(destination)
            extra = destination / "validation/bundle.json"
            extra.write_text("unexpected file", encoding="utf-8")
            with self.assertRaises(ValueError):
                verify_delivery(destination)
            extra.unlink()
            (destination / "validation/check_desktop_launcher.py").write_text("modified", encoding="utf-8")
            with self.assertRaises(ValueError):
                verify_delivery(destination)
            with self.assertRaises(FileExistsError):
                package(bundle, archive)
            self.assertEqual(sha256(archive), archive.with_name(archive.name + ".sha256").read_text().split()[0])
            archive.write_bytes(b"corrupted download")
            with self.assertRaises(ValueError):
                verify_checksum(archive)

    def test_archive_output_cannot_be_inside_the_runtime(self):
        from package_desktop_bundle import package

        with tempfile.TemporaryDirectory() as temporary:
            bundle = self.bundle(Path(temporary))
            with self.assertRaises(ValueError):
                package(bundle, bundle / "delivery.tar.gz")
            verify(bundle)

    def test_report_must_pass_and_belong_to_the_packaged_runtime(self):
        from package_desktop_bundle import package
        from check_desktop_package import verify_delivery

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = self.bundle(root)
            reports = root / "results"
            reports.mkdir()
            for name in ("launcher.json", "setup-failures.json", "parser-benchmark.json"):
                write_json(reports / name, {"status": "passed"})
            output = root / "delivery.tar.gz"
            with self.assertRaises(ValueError):
                package(bundle, output, reports)
            identity = {"revision": "test-revision", "target": "fixture", "profile": "hybrid"}
            write_json(reports / "launcher.json", {"status": "passed", "bundle": identity})
            write_json(reports / "parser-benchmark.json", {"status": "failed"})
            with self.assertRaises(ValueError):
                package(bundle, output, reports)
            self.assertFalse(output.exists())
            write_json(reports / "parser-benchmark.json", {"status": "passed"})
            package(bundle, output, reports)
            destination = root / "extracted"
            extract(output, destination)
            verify_delivery(destination)
            self.assertTrue((destination / "reports/launcher.json").is_file())


class PrebuiltInputTests(unittest.TestCase):
    def test_windows_postgres_uses_locked_private_dlls_and_omits_gui_helper(self):
        from build_desktop_bundle import install_windows_postgres_libraries

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            python = root / "python"
            numpy = python / "Lib/site-packages/numpy.libs"
            numpy.mkdir(parents=True)
            postgres = root / "postgres/bin"
            postgres.mkdir(parents=True)
            for name in ("vcruntime140.dll", "vcruntime140_1.dll"):
                (python / name).write_bytes(name.encode())
            with self.assertRaises(ValueError):
                install_windows_postgres_libraries(root)
            (numpy / "msvcp140-pinned.dll").write_bytes(b"locked C++ runtime")
            (postgres / "stackbuilder.exe").write_bytes(b"unused GUI")
            (postgres / "wxbase.dll").write_bytes(b"unused GUI library")
            install_windows_postgres_libraries(root)
            self.assertEqual((postgres / "msvcp140.dll").read_bytes(), b"locked C++ runtime")
            self.assertFalse((postgres / "stackbuilder.exe").exists())
            self.assertFalse((postgres / "wxbase.dll").exists())
            record = json.loads((root / "windows-native-libraries.json").read_text(encoding="utf-8"))
            self.assertEqual(record["msvcp140.dll"]["sha256"], sha256(postgres / "msvcp140.dll"))
            (numpy / "msvcp140-unexpected.dll").write_bytes(b"ambiguous")
            with self.assertRaises(ValueError):
                install_windows_postgres_libraries(root)

    def test_prebuilt_inputs_reject_source_tree_and_empty_wheel_directory(self):
        from build_desktop_bundle import validate_prebuilt_inputs

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            web = root / "web"
            web.mkdir()
            (web / "index.html").write_text("source entry", encoding="utf-8")
            with self.assertRaises(ValueError):
                validate_prebuilt_inputs(web, None)
            (web / "assets").mkdir()
            wheels = root / "wheels"
            wheels.mkdir()
            with self.assertRaises(ValueError):
                validate_prebuilt_inputs(web, wheels)
            (wheels / "fixture.whl").write_bytes(b"pip checks actual wheel format and pinned hashes")
            validate_prebuilt_inputs(web, wheels)
            validate_prebuilt_inputs(None, None)


if __name__ == "__main__":
    unittest.main()
