"""Native CMD regression: a successful self-removing batch must exit zero."""
import base64
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from desktop_bundle import inventory, sha256, write_json
from user_delivery import uninstall_entry, windows_uninstall_command


@unittest.skipUnless(os.name == "nt", "requires native CMD and Windows PowerShell")
class WindowsCommandTests(unittest.TestCase):
    def run_entry(self, entry):
        with tempfile.TemporaryDirectory(prefix="pm uninstall ") as temporary:
            root = Path(temporary)
            batch = root / "Uninstall.cmd"
            batch.write_bytes(entry.encode("utf-8"))
            result = subprocess.run([os.environ["COMSPEC"], "/d", "/c", str(batch)],
                                    capture_output=True, cwd=root.parent, timeout=30)
            self.assertFalse(batch.exists(), "payload did not actually delete its running batch")
            self.assertEqual((root / "proof.txt").read_text(), "executed")
            return result

    def payload(self, code):
        return (
            "$ErrorActionPreference = 'Stop'\n"
            "[IO.File]::WriteAllText((Join-Path $env:PM_UNINSTALL_ROOT 'proof.txt'), 'executed')\n"
            "Remove-Item -LiteralPath (Join-Path $env:PM_UNINSTALL_ROOT 'Uninstall.cmd')\n"
            f"exit {code}\n"
        )

    def test_released_batch_preserves_success_and_failure_status(self):
        for code in (0, 23):
            with self.subTest(code=code):
                result = self.run_entry(windows_uninstall_command(self.payload(code)))
                self.assertEqual(result.returncode, code, result.stderr.decode(errors="replace"))

    def test_old_unbuffered_entry_reproduces_false_failure(self):
        encoded = base64.b64encode(self.payload(0).encode("utf-16le")).decode("ascii")
        old = ('@echo off\r\nsetlocal\r\nset "PM_UNINSTALL_ROOT=%~dp0"\r\n'
               '"%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe" '
               f'-NoProfile -ExecutionPolicy Bypass -EncodedCommand {encoded}\r\n')
        self.assertNotEqual(self.run_entry(old).returncode, 0)


@unittest.skipUnless(os.name == "nt" and os.getenv("PAPERS_MANAGER_TEST_LAUNCHER"),
                     "requires native Windows and a built Launcher executable")
class WindowsUninstallEntryTests(unittest.TestCase):
    def test_real_entry_preserves_cancellation_failure_and_success(self):
        launcher = Path(os.environ["PAPERS_MANAGER_TEST_LAUNCHER"])
        for action in ("cancel", "reject", "remove"):
            with self.subTest(action=action), tempfile.TemporaryDirectory(prefix="pm-remove-") as temporary:
                root = Path(temporary) / "program (with spaces)"
                bundle = root / "bundle"
                bundle.mkdir(parents=True)
                shutil.copyfile(launcher, bundle / "launcher.exe")
                shutil.copyfile(launcher, root / "paper_manager.exe")
                for name in ("server.exe", "python/python.exe", "redis/redis-server.exe",
                             "postgres/bin/postgres.exe", "postgres/bin/initdb.exe",
                             "postgres/bin/pg_ctl.exe", "postgres/bin/pg_controldata.exe",
                             "web/index.html", "pdf_parser/papers_parser/server.py"):
                    path = bundle / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(b"uninstall-only fixture; never executed")
                record = {
                    "schema_version": 1, "distribution": "internal-validation-only",
                    "target": "windows-x86_64", "profile": "rules",
                    "application": {"id": "papers-manager", "display_name": "paper_manager", "version": "1.0.0"},
                    "compatibility": {"postgres_major": 17, "data_format": 1, "task_payload": 1},
                    "files": inventory(bundle),
                }
                write_json(bundle / "bundle.json", record)
                checks = [f"{item['sha256']}  {name}" for name, item in record["files"].items()]
                checks.append(f"{sha256(bundle / 'bundle.json')}  bundle.json")
                (bundle / "SHA256SUMS").write_text("\n".join(checks) + "\n", encoding="utf-8")
                (root / "data").mkdir()
                (root / "data/runtime.json").write_text("keep data", encoding="utf-8")
                (root / ".launcher_env").write_text("# keep environment", encoding="utf-8")
                (root / "personal.md").write_text("keep note", encoding="utf-8")
                name, entry = uninstall_entry("windows-x86_64")
                batch = root / name
                batch.write_bytes(entry.encode("utf-8"))
                if action == "reject":
                    # A trailing overlay leaves a runnable PE but fails identity verification.
                    with (root / "paper_manager.exe").open("ab") as stream:
                        stream.write(b"modified")
                answer = "NO" if action == "cancel" else "REMOVE"
                result = subprocess.run([os.environ["COMSPEC"], "/d", "/c", str(batch)],
                                        input=(answer + "\n\n").encode(), capture_output=True,
                                        cwd=root.parent, timeout=60)
                expected = 1 if action == "reject" else 0
                self.assertEqual(result.returncode, expected, result.stderr.decode(errors="replace"))
                self.assertEqual(bundle.exists(), action != "remove")
                self.assertEqual(batch.exists(), action != "remove")
                self.assertEqual((root / "paper_manager.exe").exists(), action != "remove")
                self.assertEqual((root / "data/runtime.json").read_text(), "keep data")
                self.assertEqual((root / ".launcher_env").read_text(), "# keep environment")
                self.assertEqual((root / "personal.md").read_text(), "keep note")


if __name__ == "__main__":
    unittest.main()
