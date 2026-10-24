#!/usr/bin/env python3
"""Exercise the real Launcher stack with explicit native dependency paths.

This validates D2 lifecycle against installed CI/developer runtimes. It is not
the D3 self-contained artifact test. All writable state belongs to this probe.
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
                if last["state"] == "failed" and expected == "ready":
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--launcher", type=Path, required=True)
    parser.add_argument("--server", type=Path, required=True)
    parser.add_argument("--postgres-bin", type=Path, required=True)
    parser.add_argument("--redis-server", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--temp-root", type=Path)
    args = parser.parse_args()
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
        with tempfile.TemporaryDirectory(prefix="pm launcher 阅读 ", dir=args.temp_root) as temporary:
            root = Path(temporary) / "private data"
            env_file = Path(temporary) / ".launcher_env"
            command = [str(args.launcher.resolve()), "--data-dir", str(root),
                       "--env-file", str(env_file), "--open-browser=false"]
            subprocess.run(command + ["--configure"], check=True, capture_output=True, timeout=15)
            config_path = root / "runtime.json"
            config = json.loads(config_path.read_text(encoding="utf-8"))
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
            base = f"http://127.0.0.1:{config['ports']['manager']}"

            def start():
                return subprocess.Popen(
                    command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                    cwd=temporary, start_new_session=os.name != "nt",
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
                ready = wait_state(base, "ready", process)
                report["steps"].append("ordinary-user first start, all real services ready")
                code, _, _ = http(base + "/stop", "POST", b"", {"Origin": "https://example.com"})
                require(code == 403, "cross-origin stop accepted")
                code, _, _ = http(base + "/status", headers={"Host": "example.com"})
                require(code == 403, "foreign management Host accepted")
                duplicate = subprocess.run(command, capture_output=True, timeout=10)
                require(duplicate.returncode != 0, "duplicate instance started")
                report["steps"].append("single instance and local management boundaries")

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
                import pymupdf
                document = pymupdf.open()
                page = document.new_page()
                page.insert_text((72, 72), "Desktop lifecycle probe", fontsize=20)
                page.insert_text((72, 110), "Methods\nA local deterministic test document.")
                pdf = document.tobytes()
                document.close()
                boundary = "papers-desktop-probe"
                body = (
                    f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; "
                    'filename="probe.pdf"\r\nContent-Type: application/pdf\r\n\r\n'
                ).encode() + pdf + f"\r\n--{boundary}--\r\n".encode()
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
                code, data, _ = http(api + f"/api/document/{document_id}/blocks")
                require(code == 200 and json.loads(data)["items"], "PDF blocks were not persisted")
                report["steps"].append("static frontend, authenticated MCP, PDF upload/Asynq/worker/reading")

                action("stop")
                wait_state(base, "stopped", process)
                require(service_ports_closed(config), "service port survived stop")
                action("start")
                wait_state(base, "ready", process)
                code, data, _ = http(markdown_url)
                require(code == 200 and b"Desktop lifecycle probe" in data, "data lost after restart")
                report["steps"].append("stop/start preserved database, PDF, blocks and Markdown")

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
                report["steps"].append("API crash cleaned dependencies; management survived; explicit retry worked")
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
        for key in ("database_password", "redis_password", "mcp_token"):
            if config.get(key):
                message = message.replace(config[key], "[redacted]")
        report["error"] = message
    (output / "launcher.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
