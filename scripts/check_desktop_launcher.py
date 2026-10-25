#!/usr/bin/env python3
"""Exercise the real Launcher stack with explicit native dependency paths.

Explicit component paths validate source/D2 runtimes. --bundle validates a sealed
D3 artifact with controlled child PATH and no source-directory dependency. All
writable state belongs to this probe.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

import psutil

# Validation must not modify a sealed harness or its read-only bundle.
sys.dont_write_bytecode = True
os.environ["PYTHONDONTWRITEBYTECODE"] = "1"

from desktop_bundle import verify


def http(url, method="GET", body=None, headers=None):
    request = urllib.request.Request(url, data=body, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            return response.status, response.read(), response.headers
    except urllib.error.HTTPError as error:
        return error.code, error.read(), error.headers


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def wait_state(base, expected, process, timeout=120):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        require(process.poll() is None, "Launcher exited before expected state")
        try:
            code, data, _ = http(base + "/status")
            if code == 200:
                last = json.loads(data)
                if last["state"] == expected:
                    return last
                if last["state"] == "failed" and expected != "failed":
                    raise RuntimeError(f"Launcher failed: {last['error']}")
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.2)
    raise RuntimeError(f"expected {expected}, last state: {last}")


def kill_owned(process):
    """Failure-only cleanup, scoped to this probe's Launcher and descendants."""
    try:
        parent = psutil.Process(process.pid)
        children = parent.children(recursive=True)
        for child in reversed(children):
            try:
                child.kill()
            except psutil.NoSuchProcess:
                pass
        parent.kill()
        psutil.wait_procs(children + [parent], timeout=10)
    except psutil.NoSuchProcess:
        pass
    process.wait(timeout=10)


def service_ports_closed(config):
    for name, port in config["ports"].items():
        if name == "manager":
            continue
        with socket.socket() as connection:
            connection.settimeout(0.1)
            if connection.connect_ex(("127.0.0.1", port)) == 0:
                return False
    return True


def upload_pdf(api, pdf):
    boundary = "papers-desktop-recovery-probe"
    body = (
        f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; "
        'filename="recovery.pdf"\r\nContent-Type: application/pdf\r\n\r\n'
    ).encode() + pdf + f"\r\n--{boundary}--\r\n".encode()
    code, data, _ = http(api + "/api/document/upload", "POST", body, {
        "Content-Type": f"multipart/form-data; boundary={boundary}",
    })
    require(code == 201, f"recovery PDF upload returned {code}")
    return json.loads(data)["document"]["id"]


def fixture_pdf(pages=1):
    text = b"BT /F1 20 Tf 72 720 Td (Desktop lifecycle probe) Tj 0 -40 Td (Methods) Tj ET"
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>",
               b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
               b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R "
               b"/Resources << /Font << /F1 5 0 R >> >> >>",
               b"<< /Length " + str(len(text)).encode() + b" >>\nstream\n" + text + b"\nendstream",
               b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    if pages > 1:
        page_ids = [3, *range(6, pages + 5)]
        kids = " ".join(f"{number} 0 R" for number in page_ids)
        objects[1] = f"<< /Type /Pages /Kids [{kids}] /Count {pages} >>".encode()
        objects.extend([objects[2]] * (pages - 1))
    result = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for index, body in enumerate(objects, 1):
        offsets.append(len(result))
        result.extend(f"{index} 0 obj\n".encode() + body + b"\nendobj\n")
    xref = len(result)
    result.extend(f"xref\n0 {len(offsets)}\n0000000000 65535 f \n".encode())
    for offset in offsets[1:]:
        result.extend(f"{offset:010d} 00000 n \n".encode())
    result.extend(f"trailer\n<< /Size {len(offsets)} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--launcher", type=Path)
    parser.add_argument("--bundle", type=Path)
    parser.add_argument("--server", type=Path)
    parser.add_argument("--postgres-bin", type=Path)
    parser.add_argument("--redis-server", type=Path)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--temp-root", type=Path)
    parser.add_argument("--ascii-paths", action="store_true", help="Windows volumes without ASCII short aliases")
    args = parser.parse_args()
    if args.ascii_paths and os.name != "nt":
        parser.error("--ascii-paths is only for the documented Windows path fallback")
    if args.bundle:
        args.bundle = args.bundle.resolve()
        manifest = verify(args.bundle)
        suffix = ".exe" if os.name == "nt" else ""
        args.launcher = args.bundle / ("launcher" + suffix)
        args.server = args.bundle / ("server" + suffix)
        args.postgres_bin = args.bundle / "postgres/bin"
        args.redis_server = args.bundle / "redis" / ("redis-server" + suffix)
    elif not all((args.launcher, args.server, args.postgres_bin, args.redis_server)):
        parser.error("use --bundle or provide all four explicit native paths")
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    report = {"status": "failed", "steps": [], "platform": sys.platform}
    process = None
    config = {}
    root = None
    try:
        if os.name != "nt":
            require(os.geteuid() != 0, "run this native probe as an ordinary user")
        else:
            import ctypes
            report["is_admin"] = bool(ctypes.windll.shell32.IsUserAnAdmin())
            require(not report["is_admin"], "run the full Launcher probe under a non-admin account")
        prefix = "pm launcher " if args.ascii_paths else "pm launcher 阅读 "
        report["path_mode"] = "ascii-fallback" if args.ascii_paths else "unicode"
        with tempfile.TemporaryDirectory(prefix=prefix, dir=args.temp_root) as temporary:
            root = Path(temporary) / "private data"
            env_file = Path(temporary) / ".launcher_env"
            launcher_env = None
            if args.bundle:
                env_file = args.bundle / ".launcher_env"
                launcher_env = {key: value for key, value in os.environ.items()
                                if key.upper() in {"SYSTEMROOT", "WINDIR", "COMSPEC", "USERPROFILE",
                                                   "HOME", "TEMP", "TMP", "LOCALAPPDATA", "APPDATA", "LANG"}}
                launcher_env["PATH"] = (
                    str(Path(os.environ.get("SYSTEMROOT", "/")) / "System32")
                    if os.name == "nt" else "/nonexistent"
                )
            command = [str(args.launcher.resolve()), "--data-dir", str(root),
                       "--env-file", str(env_file), "--open-browser=false"]
            if args.bundle:
                command += ["--install-dir", str(args.bundle)]
            if args.bundle and manifest["profile"] == "hybrid":
                python_path = args.bundle / "python" / ("python.exe" if os.name == "nt" else "bin/python3")
                model_check = (
                    "import sys,pathlib;sys.path.insert(0,sys.argv[1]);"
                    "from papers_parser.engine_factory import build_extraction_engine;"
                    "from papers_parser.crf_engine import HybridPaperExtractionEngine;"
                    "e=build_extraction_engine('crf',pathlib.Path(sys.argv[1])/'paper_crf');"
                    "assert isinstance(e,HybridPaperExtractionEngine)"
                )
                model_env = {**launcher_env, "PYTHONDONTWRITEBYTECODE": "1",
                             "LOG_PATH": str(Path(temporary) / "model-check.log")}
                subprocess.run([str(python_path), "-I", "-B", "-c", model_check,
                                str(args.bundle / "pdf_parser")], env=model_env,
                               check=True, capture_output=True, timeout=60)
                report["steps"].append("required production CRF models loaded without rules fallback")
            subprocess.run(command + ["--configure"], env=launcher_env, check=True, capture_output=True, timeout=15)
            config_path = root / "runtime.json"
            config = json.loads(config_path.read_text(encoding="utf-8"))
            if not args.bundle:
                config.update({
                    "server": str(args.server.resolve()), "python": sys.executable,
                    "parser_dir": str(args.repository.resolve() / "pdf_parser"),
                    "postgres_bin": str(args.postgres_bin.resolve()),
                    "redis": str(args.redis_server.resolve()),
                    "web_dir": str(args.repository.resolve() / "web" / "dist"),
                    "extraction_engine": "rules",
                })
                env_keys = {
                    "server": "LAUNCHER_SERVER", "python": "LAUNCHER_PYTHON",
                    "parser_dir": "LAUNCHER_PARSER_DIR", "postgres_bin": "LAUNCHER_POSTGRES_BIN",
                    "redis": "LAUNCHER_REDIS", "web_dir": "LAUNCHER_WEB_DIR",
                    "extraction_engine": "LAUNCHER_EXTRACTION_ENGINE",
                }
                env_file.write_text("\n".join(
                    key + "=" + json.dumps(config[field], ensure_ascii=False)
                    for field, key in env_keys.items()
                ) + "\n", encoding="utf-8")
            # Exercise the installer entry independently, with a recoverable
            # invalid dependency path. No data or completion marker may appear.
            failure_env = dict(os.environ if launcher_env is None else launcher_env)
            failure_env["LAUNCHER_POSTGRES_BIN"] = str(Path(temporary) / "missing-postgres")
            attempt = subprocess.run(command + ["--initialize"], env=failure_env,
                                     capture_output=True, timeout=20)
            require(attempt.returncode != 0, "invalid setup unexpectedly succeeded")
            require(not (root / "initialized.json").exists(), "failed setup published completion")
            require(not (root / "postgres").exists(), "failed validation created database data")
            # OpenConfig persists explicit launcher overrides; restore the probe's
            # original private configuration before retrying the same setup.
            config_path.write_text(json.dumps(config, indent=2), encoding="utf-8")
            base = f"http://127.0.0.1:{config['ports']['manager']}"

            def start():
                return subprocess.Popen(
                    command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                    cwd=temporary, env=launcher_env, start_new_session=os.name != "nt",
                )

            def action(name):
                code, _, _ = http(base + "/" + name, "POST", b"", {"Origin": base})
                require(code == 202, f"management action {name} returned {code}")

            def quit_launcher():
                action("quit")
                process.wait(timeout=120)
                require(process.returncode == 0, "Launcher shutdown failed")
                require(service_ports_closed(config), "service port survived Launcher exit")

            try:
                process = start()
                wait_state(base, "setup_required", process)
                require(not (root / "postgres").exists(), "startup implicitly initialized PostgreSQL")
                action("initialize")
                wait_state(base, "stopped", process)
                require((root / "initialized.json").is_file(), "setup completion marker missing")
                init_log = root / "logs" / "initdb.log"
                initialized_log = init_log.read_bytes()
                startup_at = time.monotonic()
                action("start")
                ready = wait_state(base, "ready", process)
                report["startup_seconds"] = time.monotonic() - startup_at
                report["steps"].append("ordinary-user first start, all real services ready")
                code, _, _ = http(base + "/stop", "POST", b"", {"Origin": "https://example.com"})
                require(code == 403, "cross-origin stop accepted")
                code, _, _ = http(base + "/status", headers={"Host": "example.com"})
                require(code == 403, "foreign management Host accepted")
                duplicate = subprocess.run(command, env=launcher_env, capture_output=True, timeout=10)
                require(duplicate.returncode != 0, "duplicate instance started")
                report["steps"].append("single instance and local management boundaries")

                if args.bundle:
                    children = psutil.Process(process.pid).children(recursive=True)
                    expected = {args.server.resolve(), args.redis_server.resolve(),
                                (args.postgres_bin / ("postgres.exe" if os.name == "nt" else "postgres")).resolve()}
                    paths = {Path(child.exe()).resolve() for child in children}
                    require(expected <= paths, "expected private services are not running")
                    require(all(path.is_relative_to(args.bundle) for path in paths),
                            "a runtime process came from outside the bundle")
                    report["bundle"] = {"target": manifest["target"], "profile": manifest["profile"],
                                        "revision": manifest["revision"], "runtime_paths": sorted(map(str, paths))}
                    if os.name == "nt":
                        postgres = next(child for child in children if Path(child.exe()).name == "postgres.exe")
                        mapped = {Path(item.path).name.lower(): Path(item.path).resolve()
                                  for item in postgres.memory_maps()}
                        libraries = {}
                        for name in ("vcruntime140.dll", "msvcp140.dll"):
                            expected_library = (args.postgres_bin / name).resolve()
                            require(mapped.get(name) == expected_library,
                                    f"PostgreSQL did not load private {name}")
                            libraries[name] = str(mapped[name])
                        report["bundle"]["native_libraries"] = libraries
                    report["idle_rss_bytes"] = sum(child.memory_info().rss for child in children)
                api = ready["app_url"]
                code, data, headers = http(api + "/", headers={"Accept": "text/html"})
                require(code == 200 and b"<html" in data.lower(), "static reader unavailable")
                code, _, _ = http(api + "/api/not-a-route", headers={"Accept": "text/html"})
                require(code == 404, "SPA masked an API 404")
                code, _, _ = http(ready["mcp_url"])
                require(code == 401, "MCP token configuration not active")
                initialize = json.dumps({
                    "jsonrpc": "2.0", "id": 1, "method": "initialize",
                    "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                               "clientInfo": {"name": "desktop-probe", "version": "1"}},
                }).encode()
                code, data, _ = http(ready["mcp_url"], "POST", initialize, {
                    "Authorization": "Bearer " + config["mcp_token"],
                    "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                })
                require(code == 200 and b"serverInfo" in data, "real MCP initialization failed")

                # A generated PDF has no third-party copyrighted test content.
                pdf = fixture_pdf()
                boundary = "papers-desktop-probe"
                body = (
                    f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; "
                    'filename="probe.pdf"\r\nContent-Type: application/pdf\r\n\r\n'
                ).encode() + pdf + f"\r\n--{boundary}--\r\n".encode()
                recognition_at = time.monotonic()
                code, data, _ = http(api + "/api/document/upload", "POST", body, {
                    "Content-Type": f"multipart/form-data; boundary={boundary}",
                })
                require(code == 201, f"PDF upload returned {code}: {data[:300]!r}")
                document_id = json.loads(data)["document"]["id"]
                markdown_url = api + f"/api/document/{document_id}/markdown"
                deadline = time.monotonic() + 90
                while time.monotonic() < deadline:
                    code, markdown, _ = http(markdown_url)
                    if code == 200 and b"Desktop lifecycle probe" in markdown:
                        break
                    status_url = api + f"/api/document/{document_id}/processing"
                    status_code, stage_data, _ = http(status_url)
                    if status_code == 200:
                        stages = json.loads(stage_data)["processing"]["stageStates"]
                        require(not any(stage["stage"] == "markdown" and stage["status"] == "failed"
                                        for stage in stages), "Markdown stage failed; see parser and API logs")
                    time.sleep(0.5)
                else:
                    raise RuntimeError("queued PDF did not produce readable Markdown")
                report["upload_to_read_seconds"] = time.monotonic() - recognition_at
                code, data, _ = http(api + f"/api/document/{document_id}/blocks")
                require(code == 200 and json.loads(data)["items"], "PDF blocks were not persisted")
                note_url = api + f"/api/document/{document_id}/reading-note"
                note_body = json.dumps({"markdown": "# Summary\nLocal reading note.\n## Methods\nOffline probe.",
                                        "expectedRevision": 0}).encode()
                code, note, _ = http(note_url, "PUT", note_body, {"Content-Type": "application/json"})
                require(code == 200, f"note import returned {code}: {note[:300]!r}")
                code, note, _ = http(note_url)
                require(code == 200 and b"Methods" in note, "note directory is unreadable")
                report["steps"].append("static frontend, authenticated MCP, PDF upload/Asynq/worker/reading/notes")

                action("stop")
                wait_state(base, "stopped", process)
                require(service_ports_closed(config), "service port survived stop")
                action("start")
                wait_state(base, "ready", process)
                code, data, _ = http(markdown_url)
                require(code == 200 and b"Desktop lifecycle probe" in data, "data lost after restart")
                require(init_log.read_bytes() == initialized_log, "restart unexpectedly ran initdb")
                code, note, _ = http(note_url)
                require(code == 200 and b"Methods" in note, "note directory lost after restart")
                report["steps"].append("stop/start preserved database, PDF, blocks, Markdown and notes; no initdb")

                # Interrupt an actual durable Markdown task, rather than only
                # crashing an idle server. Retry is explicit by application policy.
                recovery_id = upload_pdf(api, fixture_pdf(pages=20))
                recovery_status = api + f"/api/document/{recovery_id}/processing"
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline:
                    code, data, _ = http(recovery_status)
                    stages = json.loads(data)["processing"]["stageStates"] if code == 200 else []
                    if any(stage["stage"] == "markdown" and stage["status"] == "running"
                           for stage in stages):
                        break
                    time.sleep(0.02)
                else:
                    raise RuntimeError("recovery fixture never entered the running Markdown stage")
                children = psutil.Process(process.pid).children(recursive=True)
                servers = [child for child in children
                           if Path(child.exe()).resolve() == args.server.resolve()]
                require(len(servers) == 1, "cannot identify owned Go server")
                servers[0].kill()
                wait_state(base, "failed", process)
                require(service_ports_closed(config), "crashed API left dependent services running")
                require(http(base)[0] == 200, "management page disappeared after API crash")
                action("start")
                wait_state(base, "ready", process)
                code, data, _ = http(recovery_status)
                stages = json.loads(data)["processing"]["stageStates"] if code == 200 else []
                require(any(stage["stage"] == "markdown" and stage["status"] == "failed" for stage in stages),
                        "interrupted task was not marked failed after restart")
                code, _, _ = http(api + f"/api/document/{recovery_id}/retry", "POST", b"")
                require(code == 202, "interrupted task retry was rejected")
                deadline = time.monotonic() + 120
                while time.monotonic() < deadline:
                    code, recovered, _ = http(api + f"/api/document/{recovery_id}/markdown")
                    if code == 200 and b"Desktop lifecycle probe" in recovered:
                        break
                    time.sleep(0.5)
                else:
                    raise RuntimeError("interrupted task did not produce Markdown after explicit retry")
                report["steps"].append(
                    "API crash cleaned dependencies; interrupted task failed visibly and retry succeeded")
                quit_launcher()
                process = None

                # A conflicting service is never adopted or killed.
                with socket.socket() as occupied:
                    occupied.bind(("127.0.0.1", 0))
                    occupied.listen()
                    config["ports"]["redis"] = occupied.getsockname()[1]
                    config_path.write_text(json.dumps(config, indent=2), encoding="utf-8")
                    process = start()
                    wait_state(base, "failed", process)
                    require(occupied.fileno() >= 0, "port-conflict probe socket was closed")
                    action("quit")
                    process.wait(timeout=120)
                    require(process.returncode == 0, "port-conflict Launcher did not exit cleanly")
                    process = None
                report["steps"].append("occupied persisted port failed without adopting another service")
                # Corrupted existing data is never treated as a fresh install.
                version_file = root / "postgres/PG_VERSION"
                original_version = version_file.read_bytes()
                version_file.write_bytes(b"invalid")
                sentinel = root / "postgres/keep-test-data"
                sentinel.write_text("preserve", encoding="utf-8")
                result = subprocess.run(command + ["--initialize"], env=launcher_env,
                                        capture_output=True, timeout=20)
                require(result.returncode != 0 and sentinel.read_text() == "preserve",
                        "setup overwrote a damaged existing cluster")
                require(version_file.read_bytes() == b"invalid", "setup repaired data implicitly")
                version_file.write_bytes(original_version)
                report["steps"].append("explicit setup failure/retry; damaged data preserved")
                if args.bundle:
                    setup_probe = Path(__file__).with_name("check_desktop_setup_failures.py")
                    setup_command = [sys.executable, str(setup_probe), "--bundle", str(args.bundle),
                                     "--output", str(output / "setup-failures.json"),
                                     "--temp-root", temporary]
                    if args.ascii_paths:
                        setup_command.append("--ascii-paths")
                    subprocess.run(setup_command,
                                   env={**os.environ, "PATH": launcher_env["PATH"]},
                                   check=True, capture_output=True, timeout=180)
                    report["steps"].append("real setup filesystem failures and explicit retry")
                    benchmark = Path(__file__).with_name("benchmark_parser_shared.py")
                    python_path = args.bundle / "python" / ("python.exe" if os.name == "nt" else "bin/python3")
                    with (output / "benchmark-console.log").open("wb") as log:
                        subprocess.run([
                            sys.executable, str(benchmark), "--parser-dir", str(args.bundle / "pdf_parser"),
                            "--parser-python", str(python_path), "--engine", manifest["profile"],
                            "--jobs", "8", "--mixed-jobs", "20", "--idle-seconds", "15", "--exercise-crash",
                            "--output", str(output / "parser-benchmark.json"),
                        ], env={**os.environ, "PATH": launcher_env["PATH"], "PYTHONDONTWRITEBYTECODE": "1"},
                            stdout=log, stderr=log, check=True, timeout=300)
                    report["steps"].append(
                        "bundle c1/c4 and 20 mixed tasks: single workers, crash recovery, RSS and idle retirement"
                    )
                    verify(args.bundle)
                report["status"] = "passed"
            finally:
                if process and process.poll() is None:
                    try:
                        action("quit")
                        process.wait(timeout=120)
                    except Exception:
                        kill_owned(process)
                for log in (root / "logs").glob("*.log"):
                    text = log.read_text(encoding="utf-8", errors="replace")
                    for key in ("database_password", "redis_password", "mcp_token"):
                        text = text.replace(config[key], "[redacted]")
                    (output / log.name).write_text(text, encoding="utf-8")
    except Exception as error:
        message = f"{type(error).__name__}: {error}"
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            detail = error.stderr.decode("utf-8", errors="replace") if isinstance(error.stderr, bytes) else error.stderr
            message += "\n" + detail[-3000:]
        for key in ("database_password", "redis_password", "mcp_token"):
            if config.get(key):
                message = message.replace(config[key], "[redacted]")
        report["error"] = message
    (output / "launcher.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
