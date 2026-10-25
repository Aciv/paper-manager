#!/usr/bin/env python3
"""Exercise a managed install, synthetic upgrade and real PostgreSQL rollback."""
from __future__ import annotations

import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid

sys.dont_write_bytecode = True
os.environ["PYTHONDONTWRITEBYTECODE"] = "1"

from desktop_bundle import inventory, sha256, verify, write_json
from check_desktop_launcher import fixture_pdf, http, kill_owned, require, upload_pdf, wait_state


def reseal(root: Path, version: str):
    record = json.loads((root / "bundle.json").read_text(encoding="utf-8"))
    record["application"]["version"] = version
    record["files"] = inventory(root)
    write_json(root / "bundle.json", record)
    checks = [f"{item['sha256']}  {name}" for name, item in record["files"].items() if "sha256" in item]
    checks.append(f"{sha256(root / 'bundle.json')}  bundle.json")
    (root / "SHA256SUMS").write_text("\n".join(checks) + "\n", encoding="utf-8")
    verify(root)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--failure-probe", type=Path)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--temp-root", type=Path)
    args = parser.parse_args()
    bundle = args.bundle.resolve()
    manifest = verify(bundle)
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    suffix = ".exe" if os.name == "nt" else ""
    failure = args.failure_probe or Path(__file__).parent / ("upgrade-failure" + suffix)
    report = {"status": "failed", "platform": sys.platform, "steps": [], "synthetic_versions": True}
    process = None
    cfg = {}
    try:
        if os.name == "nt":
            import ctypes
            report["is_admin"] = bool(ctypes.windll.shell32.IsUserAnAdmin())
            require(not report["is_admin"], "run the upgrade probe as an ordinary Windows user")
        else:
            require(os.geteuid() != 0, "run the upgrade probe as an ordinary user")
        require(failure.is_file(), "validation-only native failure probe missing")
        require(manifest["application"]["version"] == "1.0.0", "fixture currently expects the 1.0.0 baseline")
        temporary = tempfile.TemporaryDirectory(prefix="pm d4 阅读 ", dir=args.temp_root)
        with temporary, ExitStack() as cleanup:
            root = Path(temporary.name)
            data, program = root / "user data 阅读", root / "program 阅读"
            shortcuts = root / "shortcuts 阅读"

            def cleanup_fixture():
                if process and process.poll() is None:
                    kill_owned(process)
                try:
                    private = json.loads((data / "runtime.json").read_text(encoding="utf-8"))
                except (OSError, ValueError):
                    private = {}
                for log in (data / "logs").glob("*.log"):
                    text = log.read_text(encoding="utf-8", errors="replace")[-12000:]
                    for key in ("database_password", "redis_password", "mcp_token"):
                        if private.get(key):
                            text = text.replace(private[key], "[redacted]")
                    (output / log.name).write_text(text, encoding="utf-8")

            cleanup.callback(cleanup_fixture)
            environment = {key: value for key, value in os.environ.items() if key.upper() in {
                "SYSTEMROOT", "WINDIR", "COMSPEC", "USERPROFILE", "HOME", "TEMP", "TMP",
                "TMPDIR", "LOCALAPPDATA", "APPDATA", "LANG",
            }}
            environment["PATH"] = (
                str(Path(os.environ["SYSTEMROOT"]) / "System32") if os.name == "nt" else "/nonexistent"
            )
            environment["PYTHONDONTWRITEBYTECODE"] = "1"
            environment["LOG_PATH"] = str(output / "maintenance.log")

            def maintain(source, operation="install", *extra, success=True):
                command = [str(source / ("launcher" + suffix)), "--maintenance", operation,
                           "--program-dir", str(program), "--data-dir", str(data),
                           "--source-bundle", str(source), "--shortcut-dir", str(shortcuts), *extra]
                with (output / "operations.log").open("ab") as log:
                    child = subprocess.Popen(command, env=environment, stdout=log, stderr=log)
                    try:
                        child.wait(timeout=600)
                    except BaseException:
                        if child.poll() is None:
                            kill_owned(child)
                        raise
                require((child.returncode == 0) == success, f"unexpected result from maintenance {operation}")

            def config():
                return json.loads((data / "runtime.json").read_text(encoding="utf-8"))

            def active_bundle():
                record = json.loads((program / "installation.json").read_text(encoding="utf-8"))
                return program / "versions" / record["active"]

            def start():
                nonlocal process, cfg
                cfg = config()
                active = active_bundle()
                process = subprocess.Popen([
                    str(active / ("launcher" + suffix)), "--data-dir", str(data),
                    "--install-dir", str(active), "--open-browser=false",
                ], env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                base = f"http://127.0.0.1:{cfg['ports']['manager']}"
                return base, wait_state(base, "ready", process)

            def stop(base):
                nonlocal process
                require(http(base + "/quit", "POST", b"", {"Origin": base})[0] == 202, "quit not accepted")
                process.wait(timeout=240)
                require(process.returncode == 0, "managed Launcher shutdown failed")
                process = None

            def sql(statement):
                pg_env = {**environment, "PGHOST": "127.0.0.1", "PGPORT": str(cfg["ports"]["postgres"]),
                          "PGUSER": "papers_local", "PGDATABASE": "postgres", "PGPASSWORD": cfg["database_password"],
                          "LD_LIBRARY_PATH": str(active_bundle() / "postgres/lib"),
                          "DYLD_LIBRARY_PATH": str(active_bundle() / "postgres/lib")}
                result = subprocess.run([str(active_bundle() / "postgres/bin" / ("psql" + suffix)),
                                         "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", statement],
                                        env=pg_env, capture_output=True, timeout=30)
                require(result.returncode == 0, "fixture SQL operation failed")
                return result.stdout.strip()

            package_root = bundle.parent
            require((package_root / "installer/install.py").is_file(), "run this probe against an exported delivery")
            private_python = bundle / "python" / ("python.exe" if os.name == "nt" else "bin/python3")
            installer = [str(private_python), "-I", "-X", "utf8", "-B",
                         str(package_root / "installer/install.py"), "install", "--no-launch",
                         "--program-dir", str(program), "--data-dir", str(data), "--shortcut-dir", str(shortcuts)]
            with (output / "installer.log").open("wb") as log:
                child = subprocess.Popen(installer, env=environment, stdout=log, stderr=log)
                try:
                    child.wait(timeout=600)
                except BaseException:
                    if child.poll() is None:
                        kill_owned(child)
                    raise
            require(child.returncode == 0, "packaged installer failed")
            original = config()
            require(any(shortcuts.iterdir()), "platform application shortcut missing")
            maintain(bundle)  # identical installer is idempotent
            report["steps"].append("per-user install/setup/probe, shortcut and idempotent reinstall")
            base, ready = start()
            api = ready["app_url"]
            document_id = upload_pdf(api, fixture_pdf())
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                code, body, _ = http(api + f"/api/document/{document_id}/markdown")
                if code == 200 and b"Desktop lifecycle probe" in body:
                    break
                time.sleep(0.2)
            else:
                raise RuntimeError("synthetic upload did not produce reading content")
            note_url = f"/api/document/{document_id}/reading-note"
            note = json.dumps({"markdown": "# Summary\nD4 fixture note.\n## Methods\nPreserve this note.",
                               "expectedRevision": 0}).encode()
            require(http(api + note_url, "PUT", note, {"Content-Type": "application/json"})[0] == 200,
                    "could not create the pre-upgrade reading note")
            token = uuid.uuid4().hex
            (data / ".d4-synthetic-fixture").write_text(token, encoding="ascii")
            sql(f"CREATE TABLE d4_upgrade_fixture(token text); INSERT INTO d4_upgrade_fixture VALUES ('{token}')")
            for path in (data / "notes/d4-canary.md", data / "uploads/d4-canary.txt"):
                path.write_bytes(b"preserve before upgrade")
            stop(base)
            report["steps"].append("real PDF/queue/parser and durable resources before upgrade")
            newer = root / "synthetic 1.0.1"
            shutil.copytree(bundle, newer, symlinks=True)
            reseal(newer, "1.0.1")
            maintain(newer)
            current = config()
            for field in ("ports", "database_password", "redis_password", "mcp_token"):
                require(current[field] == original[field], f"upgrade changed {field}")
            base, ready = start()
            require(http(ready["app_url"] + f"/api/document/{document_id}/markdown")[0] == 200,
                    "upgrade lost the parsed document")
            require(sql("SELECT token FROM d4_upgrade_fixture") == token.encode(), "upgrade lost database contents")
            require(b"Methods" in http(ready["app_url"] + note_url)[1], "upgrade lost the reading note directory")
            stop(base)
            report["steps"].append("synthetic next-version switch preserves parsed document, credentials, ports and DB")
            bad = root / "synthetic 1.0.2 failed"
            shutil.copytree(bundle, bad, symlinks=True)
            server = bad / ("server" + suffix)
            server.unlink()
            shutil.copy2(failure, server)
            reseal(bad, "1.0.2")
            maintain(bad, success=False)
            journal = json.loads((data / "maintenance/journal.json").read_text(encoding="utf-8"))
            recovery = data / "maintenance/recovery" / journal["checkpoint"] / "notes/d4-canary.md"
            require(recovery.read_bytes() == b"failed candidate wrote here",
                    "failure probe did not perform the real test migration")
            base, ready = start()
            require(sql("SELECT to_regclass('d4_failed_migration') IS NULL") == b"t",
                    "failed migration survived checkpoint rollback")
            for path in (data / "notes/d4-canary.md", data / "uploads/d4-canary.txt"):
                require(path.read_bytes() == b"preserve before upgrade", "failed migration changed durable files")
            require(http(ready["app_url"] + f"/api/document/{document_id}/markdown")[0] == 200,
                    "recovery lost the parsed document")
            require(b"Methods" in http(ready["app_url"] + note_url)[1], "recovery lost the reading note")
            stop(base)
            report["steps"].append("real failed SQL migration and file mutations restored from cold checkpoint")
            maintain(bundle, "cleanup")
            if os.name == "nt":
                powershell = Path(os.environ["SYSTEMROOT"]) / "System32/WindowsPowerShell/v1.0/powershell.exe"
                uninstaller = [str(powershell),
                               "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(program / "uninstall.ps1")]
            else:
                uninstaller = ["/bin/sh", str(program / "uninstall")]
            with (output / "uninstaller.log").open("wb") as log:
                child = subprocess.Popen(uninstaller, env=environment, stdout=log, stderr=log)
                try:
                    child.wait(timeout=180)
                except BaseException:
                    if child.poll() is None:
                        kill_owned(child)
                    raise
            require(child.returncode == 0, "installed uninstaller failed")
            require((data / "runtime.json").is_file() and (data / "notes/d4-canary.md").is_file(),
                    "uninstall removed user data")
            installed = json.loads((program / "installation.json").read_text(encoding="utf-8"))
            require(not installed["active"], "uninstall left active program")
            maintain(newer)
            base, ready = start()
            require(http(ready["app_url"] + f"/api/document/{document_id}/markdown")[0] == 200,
                    "reinstall could not reuse preserved data")
            stop(base)
            report["steps"].append("owned cleanup, uninstall preserves data, exact-version reinstall reuses data")
            report["status"] = "passed"
    except Exception as error:
        message = f"{type(error).__name__}: {error}"
        for name in ("database_password", "redis_password", "mcp_token"):
            if cfg.get(name):
                message = message.replace(cfg[name], "[redacted]")
        report["error"] = message
    finally:
        if process and process.poll() is None:
            kill_owned(process)
    write_json(output / "upgrade.json", report)
    print(json.dumps(report, indent=2))
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
