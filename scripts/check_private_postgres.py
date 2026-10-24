#!/usr/bin/env python3
"""Exercise explicitly selected PostgreSQL binaries without installing a service.

Creates and removes only its own TemporaryDirectory. Never connects to a
pre-existing cluster. Run as an ordinary user; PostgreSQL rejects root.
"""
from __future__ import annotations

import argparse
import json
import os
import platform
import secrets
import signal
import socket
import shutil
import subprocess
import tempfile
from pathlib import Path


def native_cluster_path(path: Path) -> Path:
    """EDB Windows CLI uses ANSI argv; retain Unicode roots through 8.3 aliases.

    This probe records the alias requirement. It does not claim the binaries
    accept arbitrary UTF-8 argv or that every volume enables short names.
    """
    if os.name != "nt" or str(path).isascii():
        return path
    import ctypes
    from ctypes import wintypes
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.GetShortPathNameW.argtypes = [wintypes.LPCWSTR, wintypes.LPWSTR, wintypes.DWORD]
    kernel.GetShortPathNameW.restype = wintypes.DWORD
    result = ctypes.create_unicode_buffer(32768)
    size = kernel.GetShortPathNameW(str(path), result, len(result))
    if not size or size >= len(result) or not result.value.isascii():
        raise RuntimeError("PostgreSQL requires an ANSI-compatible path; this volume has no usable short path")
    return Path(result.value)


def probe(bin_dir: Path, output: Path, temp_root: Path | None = None) -> int:
    suffix = ".exe" if os.name == "nt" else ""
    binaries = {name: bin_dir.resolve() / (name + suffix) for name in ("initdb", "pg_ctl", "psql")}
    report = {"status": "failed", "platform": platform.platform(), "steps": []}
    if os.name == "nt":
        import ctypes
        report["is_admin"] = bool(ctypes.windll.shell32.IsUserAnAdmin())
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        if hasattr(os, "geteuid") and os.geteuid() == 0:
            raise RuntimeError("run as an ordinary user, not root")
        if any(not path.is_file() for path in binaries.values()):
            raise FileNotFoundError(f"missing PostgreSQL binaries in {bin_dir}")
        with tempfile.TemporaryDirectory(prefix="pm postgres 验证 ", dir=temp_root) as temporary:
            root = native_cluster_path(Path(temporary))
            report["path_mode"] = "Windows short alias" if str(root) != temporary else "native path"
            data = root / "data"
            password = root / "password"
            secret = secrets.token_hex(24)
            password.write_text(secret + "\n", encoding="utf-8")
            password.chmod(0o600)
            with socket.socket() as candidate:
                candidate.bind(("127.0.0.1", 0))
                port = candidate.getsockname()[1]
            # Suppress ambient libpq connection settings; use explicit target.
            env = {key: value for key, value in os.environ.items() if not key.startswith("PG")}
            env["PGPASSWORD"] = secret

            sequence = 0

            def run(name, *arguments):
                nonlocal sequence
                sequence += 1
                stdout = root / f"command-{sequence}.stdout"
                stderr = root / f"command-{sequence}.stderr"
                # Native helpers inherit stdio. PIPE + communicate on Windows
                # can wait forever after killing only the command on timeout.
                with stdout.open("wb") as out, stderr.open("wb") as err:
                    command = subprocess.Popen([str(binaries[name]), *map(str, arguments)],
                                               cwd=root, env=env, stdout=out, stderr=err,
                                               start_new_session=os.name != "nt")
                    try:
                        command.wait(timeout=40)
                    except subprocess.TimeoutExpired:
                        if os.name == "nt":
                            subprocess.run(["taskkill", "/PID", str(command.pid), "/T", "/F"],
                                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                           timeout=5, check=False)
                        else:
                            os.killpg(command.pid, signal.SIGKILL)
                        command.kill()
                        command.wait(timeout=5)
                        raise RuntimeError(f"{name} timed out; "
                                           f"{stderr.read_text(errors='replace')[-2000:]}")
                result = stdout.read_text(encoding="utf-8", errors="replace")
                if command.returncode:
                    raise RuntimeError(f"{name} exit={command.returncode}: "
                                       f"{stderr.read_text(encoding='utf-8', errors='replace')[-2000:]} {result[-2000:]}")
                return result.strip()

            report["version"] = run("initdb", "--version")
            run("initdb", "-D", data, "-U", "papers_probe", "--pwfile", password,
                "--auth=scram-sha-256", "--encoding=UTF8", "--no-locale")
            report["steps"].append("initdb under ordinary user")
            # Explicit loopback plus private socket location on Unix.
            options = f"-h 127.0.0.1 -p {port}"
            if os.name != "nt":
                options += f' -k "{root}"'
            try:
                run("pg_ctl", "-D", data, "-l", root / "postgres.log", "-w", "-t", "30",
                    "-o", options, "start")
                report["steps"].append("start")
                psql = ("-X", "-w", "-h", "127.0.0.1", "-p", str(port), "-U", "papers_probe",
                        "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-At")
                run("psql", *psql, "-c", "CREATE TABLE probe(value text); INSERT INTO probe VALUES ('persisted');")
                run("pg_ctl", "-D", data, "-w", "-t", "30", "-m", "fast", "stop")
                report["steps"].append("write and stop")
                run("pg_ctl", "-D", data, "-l", root / "postgres.log", "-w", "-t", "30",
                    "-o", options, "start")
                if run("psql", *psql, "-c", "SELECT value FROM probe;") != "persisted":
                    raise RuntimeError("data did not survive restart")
                report["steps"].append("restart and verify persisted SQL data")
            finally:
                # The target cluster is exclusively this probe's fresh directory.
                if (data / "postmaster.pid").exists():
                    run("pg_ctl", "-D", data, "-w", "-t", "30", "-m", "fast", "stop")
            report["steps"].append("final stop")
        report["status"] = "passed"
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"{report['status']}: {output}")
    return int(report["status"] != "passed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, default=Path(shutil.which("pg_ctl") or "/missing/pg_ctl").parent)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--temp-root", type=Path)
    args = parser.parse_args()
    raise SystemExit(probe(args.bin_dir, args.output, args.temp_root))
