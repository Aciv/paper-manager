"""Desktop packaging and validation tests consolidated from the former per-tool modules."""

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
import contextlib
import importlib.util
from unittest import mock
import copy

# ---------------------------------------------------------------------------
# Consolidated from test_desktop_bundle.py.
# ---------------------------------------------------------------------------

class BundleTests(unittest.TestCase):
    def test_sealed_bundle_detects_content_addition_and_manifest_changes(self):
        """Verify that adding an unlisted file or modifying a listed file makes verification fail."""
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
        """Verify that zip and tar entries using parent paths are rejected without writing outside the destination."""
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
        """Verify that prefix-filtered extraction keeps selected Postgres files and drops unused embedded applications."""
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
        """Verify that inventory rejects a bundle symlink pointing outside the bundle."""
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
    def bundle(self, root, target="fixture"):
        bundle = root / "bundle"
        bundle.mkdir()
        (bundle / "program").write_bytes(b"isolated runtime fixture")
        launcher = bundle / ("launcher.exe" if target == "windows-x86_64" else "launcher")
        launcher.write_bytes(b"native launcher fixture")
        record = {"schema_version": 1, "distribution": "internal-validation-only", "target": target,
                  "application": {"id": "papers-manager", "display_name": "paper_manager", "version": "1.0.0"},
                  "revision": "test-revision", "profile": "hybrid",
                  "launcher_layout": "nested-bundle-v1", "files": inventory(bundle)}
        write_json(bundle / "bundle.json", record)
        checks = "\n".join(
            [f"{item['sha256']}  {name}" for name, item in record["files"].items()]
            + [f"{sha256(bundle / 'bundle.json')}  bundle.json"]
        ) + "\n"
        (bundle / "SHA256SUMS").write_text(checks, encoding="utf-8")
        return bundle

    def test_windows_zip_exposes_verified_native_launcher(self):
        """Verify that a Windows zip exposes the sealed launcher as paper_manager.exe and rejects re-indexed replacements."""
        from package_desktop_bundle import package
        from check_desktop_package import verify_delivery

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = self.bundle(root, "windows-x86_64")
            archive = package(bundle, root / "paper_manager.zip")
            extracted = root / "unpacked"
            extract(archive, extracted)
            record = verify_delivery(extracted)
            self.assertEqual(record["entrypoint"], "paper_manager.exe")
            self.assertEqual(
                (extracted / "paper_manager.exe").read_bytes(),
                (bundle / "launcher.exe").read_bytes(),
            )
            # Even a re-indexed outer launcher cannot replace the sealed one.
            (extracted / "paper_manager.exe").write_bytes(b"different binary")
            record["files"]["paper_manager.exe"]["sha256"] = sha256(extracted / "paper_manager.exe")
            write_json(extracted / "package.json", record)
            with self.assertRaisesRegex(ValueError, "sealed runtime"):
                verify_delivery(extracted)

    def test_legacy_runtime_needs_explicit_maintenance_export(self):
        """Verify that a bundle without a launcher layout requires the maintenance layout and exports no entrypoint."""
        from package_desktop_bundle import package
        from check_desktop_package import verify_delivery

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = self.bundle(root)
            record = json.loads((bundle / "bundle.json").read_text(encoding="utf-8"))
            record.pop("launcher_layout")
            write_json(bundle / "bundle.json", record)
            checks = (bundle / "SHA256SUMS").read_text(encoding="utf-8").splitlines()
            checks[-1] = f"{sha256(bundle / 'bundle.json')}  bundle.json"
            (bundle / "SHA256SUMS").write_text("\n".join(checks) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "rebuild"):
                package(bundle, root / "not-compatible.tar.gz")
            archive = package(bundle, root / "legacy.tar.gz", layout="maintenance")
            extract(archive, root / "extracted")
            self.assertNotIn("entrypoint", verify_delivery(root / "extracted"))

    def test_exported_delivery_detects_modified_harness_and_unknown_files(self):
        """Verify that an exported delivery verifies end-to-end and rejects modified files, metadata changes, and unknown extras."""
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
            exported = verify_delivery(destination)
            self.assertEqual(exported["entrypoint"], "paper_manager")
            self.assertEqual(
                (destination / "paper_manager").read_bytes(), (bundle / "launcher").read_bytes(),
            )
            if os.name != "nt":
                self.assertTrue(os.access(destination / "paper_manager", os.X_OK))
            guide = destination / "README.md"
            original_guide = guide.read_bytes()
            self.assertIn(b"1.0.0", original_guide)
            self.assertTrue((destination / "LICENSE").is_file())
            guide.write_bytes(b"modified installation instructions")
            with self.assertRaises(ValueError):
                verify_delivery(destination)
            guide.write_bytes(original_guide)
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
        """Verify that packaging refuses an archive destination inside the bundle and leaves the runtime valid."""
        from package_desktop_bundle import package

        with tempfile.TemporaryDirectory() as temporary:
            bundle = self.bundle(Path(temporary))
            with self.assertRaises(ValueError):
                package(bundle, bundle / "delivery.tar.gz")
            verify(bundle)

    def test_report_must_pass_and_belong_to_the_packaged_runtime(self):
        """Verify that packaging accepts only passing reports matching the bundle identity, then ships them."""
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
        """Verify that Windows Postgres staging requires one pinned C++ runtime DLL, records its hash, and drops GUI helpers."""
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
        """Verify that prebuilt validation rejects source web trees and empty wheel directories while accepting built inputs."""
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

# ---------------------------------------------------------------------------
# Consolidated from test_desktop_installer.py.
# ---------------------------------------------------------------------------

spec = importlib.util.spec_from_file_location(
    "desktop_installer", Path(__file__).resolve().parents[3] / "packaging/archive/installer/install.py"
)
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class DesktopInstallerTest(unittest.TestCase):
    def install_command(self, *options):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            program, data, shortcuts = root / "program", root / "data", root / "shortcuts"
            program.mkdir()
            (program / "installation.json").write_text(json.dumps({"active": "1.0.0-fixture"}), encoding="utf-8")
            argv = ["install.py", "install", "--no-launch", *options]
            with mock.patch.object(installer.sys, "argv", argv), \
                    mock.patch.object(installer, "default_paths", return_value=(program, data, shortcuts)), \
                    mock.patch.object(installer.subprocess, "run") as maintenance, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(installer.main(), 0)
                command = maintenance.call_args.args[0]
            self.assertFalse(shortcuts.exists())
            return command, str(shortcuts.resolve())

    def test_shortcut_is_opt_in(self):
        """Verify that installation omits shortcut arguments by default and passes the resolved directory only when requested."""
        command, _ = self.install_command()
        self.assertNotIn("--shortcut-dir", command)
        command, shortcuts = self.install_command("--create-shortcut")
        index = command.index("--shortcut-dir")
        self.assertEqual(command[index + 1], shortcuts)

    def test_explicit_shortcut_directory_is_also_opt_in(self):
        """Verify that an explicit shortcut directory is forwarded as its resolved path only when requested."""
        with tempfile.TemporaryDirectory() as temporary:
            shortcuts = Path(temporary) / "chosen"
            command, _ = self.install_command("--shortcut-dir", str(shortcuts))
            index = command.index("--shortcut-dir")
            self.assertEqual(command[index + 1], str(shortcuts.resolve()))

    def test_default_windows_install_never_starts_desktop_lookup(self):
        """Verify that default Windows paths skip the desktop lookup and return no shortcut directory."""
        with mock.patch.object(installer.sys, "platform", "win32"), \
                mock.patch.dict(installer.os.environ, {"LOCALAPPDATA": "local", "APPDATA": "data"}), \
                mock.patch.object(installer.subprocess, "check_output") as lookup:
            _, _, shortcuts = installer.default_paths()
            self.assertIsNone(shortcuts)
            lookup.assert_not_called()

    def test_optional_windows_desktop_lookup_failure_is_nonfatal(self):
        """Verify that a failing optional Windows desktop lookup warns and continues without a shortcut directory."""
        with mock.patch.object(installer.sys, "platform", "win32"), \
                mock.patch.dict(installer.os.environ, {
                    "LOCALAPPDATA": "local", "APPDATA": "data", "SYSTEMROOT": "windows",
                }), \
                mock.patch.object(installer.subprocess, "check_output", side_effect=OSError("fixture")), \
                contextlib.redirect_stderr(io.StringIO()) as warnings:
            _, _, shortcuts = installer.default_paths(include_shortcut=True)
            self.assertIsNone(shortcuts)
            self.assertIn("shortcut skipped", warnings.getvalue())

    def test_windows_installer_accepts_ascii_paths_and_rejects_non_ascii(self):
        """Verify that Windows install paths must be ASCII, rejecting non-ASCII program or data directories."""
        with mock.patch.object(installer.sys, "platform", "win32"):
            installer.validate_install_paths(Path("program"), Path("data"))
            for program, data in ((Path("程序"), Path("data")), (Path("program"), Path("数据"))):
                with self.subTest(program=program, data=data):
                    with self.assertRaisesRegex(ValueError, "--program-dir"):
                        installer.validate_install_paths(program, data)

    def test_windows_installer_rejects_path_before_running_maintenance(self):
        """Verify that invalid Windows paths abort installation before maintenance runs and create no directories."""
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            program, data = root / "程序", root / "data"
            argv = ["install.py", "install", "--program-dir", str(program), "--data-dir", str(data)]
            with mock.patch.object(installer.sys, "platform", "win32"), \
                    mock.patch.object(installer.sys, "argv", argv), \
                    mock.patch.object(installer, "default_paths", return_value=(program, data, root / "shortcuts")), \
                    mock.patch.object(installer.subprocess, "run") as maintenance:
                with self.assertRaisesRegex(ValueError, "Windows 安装目录"):
                    installer.main()
                maintenance.assert_not_called()
            self.assertFalse(program.exists())
            self.assertFalse(data.exists())

    def test_unix_installer_keeps_unicode_path_support(self):
        """Verify that non-Windows installations accept Unicode program and data paths."""
        with mock.patch.object(installer.sys, "platform", "linux"):
            installer.validate_install_paths(Path("程序"), Path("数据"))


if __name__ == "__main__":
    unittest.main()

# ---------------------------------------------------------------------------
# Consolidated from test_desktop_validation.py.
# ---------------------------------------------------------------------------

def load(name):
    candidates = [Path(__file__).resolve().parents[1] / (name + ".py"),
                  Path(__file__).resolve().parents[2] / "parser" / (name + ".py")]
    source = next(path for path in candidates if path.is_file())
    spec = importlib.util.spec_from_file_location(name, source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


benchmark = load("benchmark_parser_shared")
dependencies = load("check_desktop_dependencies")
postgres = load("check_private_postgres")
integration = load("run_desktop_integration")
launcher = load("check_desktop_launcher")


class DesktopValidationTest(unittest.TestCase):
    def baseline(self):
        return {
            "fingerprint": {"host": "fixture", "jobs": 8},
            "cases": [{
                "concurrency": 4, "ready_seconds": 1.0,
                "peak_tree_rss": 500 << 20, "peak_parent_rss": 40 << 20,
                "idle_after_retirement": {"parent_rss": 40 << 20, "tree_rss": 40 << 20},
                "metrics": {"recognize": {"jobs_per_second": 4, "p95_seconds": 1}},
            }],
        }

    def test_small_noise_does_not_trip_gate(self):
        """Verify that a small parent-RSS fluctuation within tolerance produces no benchmark gate failures."""
        baseline = self.baseline()
        current = copy.deepcopy(baseline)
        current["cases"][0]["peak_parent_rss"] += 4 << 20
        self.assertEqual(benchmark.compare(current, baseline), [])

    def test_performance_and_memory_regressions_fail_independently(self):
        """Verify that throughput, p95, peak-tree, and idle regressions each trigger their own gate failure."""
        baseline = self.baseline()
        changes = [
            ("throughput", lambda case: case["metrics"]["recognize"].update(jobs_per_second=2)),
            ("p95", lambda case: case["metrics"]["recognize"].update(p95_seconds=2)),
            ("peak_tree_rss", lambda case: case.update(peak_tree_rss=700 << 20)),
            ("idle", lambda case: case["idle_after_retirement"].update(parent_rss=80 << 20)),
        ]
        for expected, change in changes:
            with self.subTest(expected=expected):
                current = copy.deepcopy(baseline)
                change(current["cases"][0])
                self.assertTrue(any(expected in failure for failure in benchmark.compare(current, baseline)))

    def test_different_host_or_load_is_not_a_valid_comparison(self):
        """Verify that comparisons across differing hosts or concurrency loads are rejected as invalid."""
        baseline = self.baseline()
        current = copy.deepcopy(baseline)
        current["fingerprint"]["host"] = "other"
        with self.assertRaises(ValueError):
            benchmark.compare(current, baseline)
        current = copy.deepcopy(baseline)
        current["cases"][0]["concurrency"] = 1
        with self.assertRaises(ValueError):
            benchmark.compare(current, baseline)

    def test_aggregate_uses_three_run_median_and_keeps_noise_range(self):
        """Verify that aggregation uses the median of three runs, records observed ranges, and rejects fewer runs."""
        reports = [self.baseline() for _ in range(3)]
        for report, latency in zip(reports, (1.0, 1.1, 3.0)):
            report["cases"].append(copy.deepcopy(report["cases"][0]))
            report["cases"][0]["concurrency"] = 1
            report["cases"][0]["metrics"]["recognize"]["p95_seconds"] = latency
        summary = benchmark.aggregate(reports)
        self.assertEqual(summary["cases"][0]["metrics"]["recognize"]["p95_seconds"], 1.1)
        self.assertEqual(summary["observed_ranges"]["1"]["recognize"]["p95_seconds"]["max"], 3.0)
        with self.assertRaises(ValueError):
            benchmark.aggregate(reports[:2])

    def test_streamed_digest_detects_modified_artifact(self):
        """Verify that streamed digesting yields a stable hash that changes when the artifact is modified."""
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "artifact"
            path.write_bytes(b"candidate")
            digest = dependencies.sha256(path)
            path.write_bytes(b"modified")
            self.assertNotEqual(dependencies.sha256(path), digest)

    def test_initialization_failure_stops_waiting_immediately(self):
        """Verify that a launcher reporting a failed state raises immediately without sleeping through the timeout."""
        process = mock.Mock()
        process.poll.return_value = None
        failure = json.dumps({"state": "failed", "error": "fixture initialization failure"}).encode()
        with mock.patch.object(launcher, "http", return_value=(200, failure, {})), \
                mock.patch.object(launcher.time, "monotonic", side_effect=[0, 0, 121]), \
                mock.patch.object(launcher.time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "Launcher failed: fixture initialization failure"):
                launcher.wait_state("http://fixture", "stopped", process)
            sleep.assert_not_called()

    def test_postgres_preflight_reports_failure_without_connecting(self):
        """Verify that a missing Postgres binary yields a failed report with no steps and no connection attempt."""
        import json
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "result.json"
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(postgres.probe(Path(temporary) / "missing-bin", output), 1)
            report = json.loads(output.read_text())
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["steps"], [])

    def test_native_gate_requires_actual_pass_events_and_rejects_skip(self):
        """Verify that the native gate requires a pass event for every required test and rejects skipped tests."""
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "events.jsonl"
            events = [{"Action": "pass", "Test": name} for name in integration.REQUIRED_TESTS]
            path.write_text("\n".join(json.dumps(event) for event in events))
            self.assertEqual(set(integration.verify_events(path)), integration.REQUIRED_TESTS)
            path.write_text("\n".join(json.dumps(event) for event in events[1:]))
            with self.assertRaises(RuntimeError):
                integration.verify_events(path)
            events.append({"Action": "skip", "Test": "TestSomeOtherDatabasePostgres"})
            path.write_text("\n".join(json.dumps(event) for event in events))
            with self.assertRaises(RuntimeError):
                integration.verify_events(path)

    def test_sampler_counts_helper_memory_without_duplicate_worker(self):
        """Verify that helper RSS is included in the tree total without listing the helper as a worker."""
        parent = mock.Mock(pid=100)
        parent.memory_info.return_value.rss = 10
        worker, helper = mock.Mock(pid=101), mock.Mock(pid=102)
        for child, ppid, rss in ((worker, 100, 20), (helper, 101, 30)):
            child.cmdline.return_value = ["python", "-m", "papers_parser.worker", "--kind", "recognize"]
            child.ppid.return_value = ppid
            child.memory_info.return_value.rss = rss
            child.num_threads.return_value = 2
        parent.children.return_value = [worker, helper]
        observed = benchmark.snapshot(parent)
        self.assertEqual([item["pid"] for item in observed["workers"]], [101])
        self.assertEqual([item["pid"] for item in observed["helpers"]], [102])
        self.assertEqual(observed["tree_rss"], 60)

    def test_sampler_ignores_only_confirmed_zombies_after_access_denied(self):
        """Verify that access-denied children are skipped only when confirmed zombies, otherwise the error propagates."""
        parent, helper = mock.Mock(pid=100), mock.Mock(pid=101)
        parent.memory_info.return_value.rss = 10
        parent.children.return_value = [helper]
        helper.cmdline.side_effect = benchmark.psutil.AccessDenied(pid=101)
        helper.is_running.return_value = True
        helper.status.return_value = benchmark.psutil.STATUS_ZOMBIE
        self.assertEqual(benchmark.snapshot(parent)["tree_rss"], 10)
        helper.status.return_value = benchmark.psutil.STATUS_RUNNING
        with self.assertRaises(benchmark.psutil.AccessDenied):
            benchmark.snapshot(parent)


if __name__ == "__main__":
    unittest.main()
