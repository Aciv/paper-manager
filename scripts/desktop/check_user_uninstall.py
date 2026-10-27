#!/usr/bin/env python3
"""Exercise the actual user entry, stopped-service settings and uninstall."""
from __future__ import annotations

import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile

import psutil

from check_desktop_launcher import http, kill_owned, require, wait_state
from desktop_bundle import verify


def free_port(exclude):
    for _ in range(20):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        if port not in exclude:
            return port
    raise RuntimeError("could not select an unused fixture port")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--delivery", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--temp-root", type=Path)
    args = parser.parse_args()
    delivery, output = args.delivery.resolve(), args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    report = {"status": "failed", "steps": [], "path_mode": "ascii"}
    process = None
    try:
        manifest = verify(delivery / "bundle")
        windows = os.name == "nt"
        suffix = ".exe" if windows else ""
        script_name = {
            "windows-x86_64": "Uninstall.cmd", "macos-arm64": "Uninstall.command",
            "linux-x86_64": "Uninstall.sh",
        }[manifest["target"]]
        with tempfile.TemporaryDirectory(prefix="pm-user-", dir=args.temp_root) as temporary, ExitStack() as cleanup:
            root = Path(temporary)
            program = root / "program"
            data = program / "data"
            shutil.copytree(delivery, program, symlinks=True)
            env = {key: value for key, value in os.environ.items() if key.upper() in {
                "SYSTEMROOT", "WINDIR", "COMSPEC", "USERPROFILE", "HOME", "TEMP", "TMP",
                "TMPDIR", "LOCALAPPDATA", "APPDATA", "LANG",
            }}
            env["PATH"] = str(Path(env.get("SYSTEMROOT", "/")) / "System32") if windows else "/nonexistent"
            environment_path = program / ".launcher_env"
            initial_port = free_port([])
            environment_path.write_text(
                "LAUNCHER_OPEN_BROWSER=false\n", encoding="utf-8",
            )
            launcher_path = program / "launcher.json"
            launcher_settings = json.loads(launcher_path.read_text(encoding="utf-8"))
            launcher_settings["ports"]["api"] = initial_port
            launcher_path.write_text(json.dumps(launcher_settings), encoding="utf-8")
            environment_bytes = environment_path.read_bytes()
            command = [str(program / ("paper_manager" + suffix))]
            with (output / "configure-console.log").open("wb") as log:
                subprocess.run(command + ["--configure"], env=env, check=True,
                               stdout=log, stderr=log, timeout=30)
            config_path = data / "runtime.json"
            require(config_path.is_file(), "portable Launcher did not use program/data")
            original = json.loads(config_path.read_text(encoding="utf-8"))
            require(original["ports"]["api"] == initial_port, "root launcher.json was not loaded")
            base = f"http://127.0.0.1:{original['ports']['manager']}"
            with (output / "launcher-console.log").open("wb") as log:
                process = subprocess.Popen(command, env=env, stdout=log, stderr=log, cwd=root)
                cleanup.callback(lambda: kill_owned(process) if process and process.poll() is None else None)
                wait_state(base, "setup_required", process)

                def request(path, method="GET", body=None):
                    headers = {"Origin": base}
                    payload = b"" if body is None else json.dumps(body).encode()
                    if body is not None:
                        headers["Content-Type"] = "application/json"
                    code, raw, _ = http(base + path, method, payload if method != "GET" else None, headers)
                    return code, raw

                code, raw = request("/configuration")
                require(code == 200, "configuration read failed before initialization")
                settings = json.loads(raw)
                require(settings["editable"], "settings disabled while only Launcher is running")
                settings["ports"]["api"] = free_port(original["ports"].values())
                settings["concurrency"] = {"tasks": 16, "recognition": 2, "extraction": 2, "render": 2, "rpcs": 4}
                payload = {key: settings[key] for key in ("ports", "concurrency", "extraction_engine")}
                require(request("/configuration", "PUT", payload)[0] == 200, "stopped settings save failed")
                saved_ports = json.loads(launcher_path.read_text(encoding="utf-8"))["ports"]
                require(saved_ports == payload["ports"], "management edit did not update launcher.json")
                require(request("/initialize", "POST")[0] == 202, "initialization refused after settings save")
                wait_state(base, "stopped", process)
                require(request("/start", "POST")[0] == 202, "stopped state published before start admission")
                ready = wait_state(base, "ready", process)
                require(ready["app_url"].endswith(str(payload["ports"]["api"])), "saved API port not applied")
                children = psutil.Process(process.pid).children(recursive=True)
                api = next(child for child in children if Path(child.exe()).name == "server" + suffix)
                require(api.environ().get("ASYNQ_CONCURRENCY") == "16", "Go task limit not applied")
                require(api.environ().get("DOCUMENT_RECOGNITION_CONCURRENCY") == "2", "Go parser admission not applied")
                child = next(child for child in children if "papers_parser.server" in child.cmdline())
                args_list = child.cmdline()
                require(args_list[args_list.index("--recognize-concurrency") + 1] == "2", "parser limit not applied")
                require(args_list[args_list.index("--max-concurrent-rpcs") + 1] == "4", "RPC budget not applied")
                require(args_list[args_list.index("--execution-mode") + 1] == "shared", "shared worker mode changed")
                before = config_path.read_bytes()
                require(request("/configuration", "PUT", payload)[0] == 409, "running settings were writable")
                require(config_path.read_bytes() == before, "rejected settings altered file")
                report["steps"].append("Launcher-only configuration saves and reaches real service ports/admission")

                def uninstall(answer):
                    script = program / script_name
                    invocation = [env["COMSPEC"], "/d", "/c", str(script)] if windows else ["/bin/sh", str(script)]
                    return subprocess.run(invocation, input=(answer + "\n\n").encode(), env=env,
                                          cwd=root, capture_output=True, timeout=120)

                require(uninstall("REMOVE").returncode != 0, "active program was removed")
                require((program / "bundle").is_dir(), "active bundle disappeared")
                require((program / "skills/papers-manager-reading/SKILL.md").is_file(), "active skills disappeared")
                require(request("/stop", "POST")[0] == 202, "stop refused")
                wait_state(base, "stopped", process)
                disk = json.loads(config_path.read_text(encoding="utf-8"))
                disk["concurrency"]["extraction"] = 1
                config_path.write_text(json.dumps(disk, indent=2), encoding="utf-8")
                require(request("/start", "POST")[0] == 202, "file-edited settings refused")
                wait_state(base, "ready", process)
                children = psutil.Process(process.pid).children(recursive=True)
                api = next(child for child in children if Path(child.exe()).name == "server" + suffix)
                require(api.environ().get("DOCUMENT_EXTRACTION_CONCURRENCY") == "1", "JSON edit not reloaded")
                require(request("/quit", "POST")[0] == 202, "quit refused")
                process.wait(timeout=180)
                require(process.returncode == 0, "Launcher exit failed")
                process = None
                saved = config_path.read_bytes()
                (program / "personal.md").write_text("keep", encoding="utf-8")
                require(uninstall("NO").returncode == 0, "cancel failed")
                require((program / "bundle").is_dir(), "cancel removed program")
                removed = uninstall("REMOVE")
                require(removed.returncode == 0, "uninstall failed: " + removed.stderr.decode(errors="replace")[-1500:])
                require(not (program / "bundle").exists(), "bundle retained")
                require(not (program / "skills").exists(), "unmodified shipped skills retained")
                require(not (program / ("paper_manager" + suffix)).exists(), "native entry retained")
                require((program / "personal.md").read_text() == "keep", "unrelated note removed")
                require(config_path.read_bytes() == saved and (data / "postgres/PG_VERSION").is_file(),
                        "database/configuration not preserved")
                require(environment_path.read_bytes() == environment_bytes, "root environment file removed")
                report["steps"].extend([
                    "running settings and active uninstall rejected",
                    "stopped JSON edits loaded on next start; shared architecture preserved",
                    "cancel retains program; confirmed native uninstall preserves DB/config and unrelated notes",
                ])
                report["status"] = "passed"
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    finally:
        if process and process.poll() is None:
            process.kill()
            process.wait(timeout=20)
        (output / "uninstall.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
