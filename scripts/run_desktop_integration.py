#!/usr/bin/env python3
"""Run real Go integration tests against an owned native Redis and supplied PG.

No Docker and no pre-existing Redis. CI supplies an isolated PostgreSQL cluster.
The JSON event gate rejects silently skipped database/queue coverage.
"""
from __future__ import annotations

import argparse
import json
import os
import secrets
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time


def redis_command(port, *arguments):
    with socket.create_connection(("127.0.0.1", port), timeout=3) as connection:
        parts = [str(argument).encode() for argument in arguments]
        connection.sendall(b"*%d\r\n" % len(parts) + b"".join(
            b"$%d\r\n" % len(part) + part + b"\r\n" for part in parts))
        with connection.makefile("rb") as stream:
            line = stream.readline()
            if line.startswith(b"+"):
                return line[1:-2].decode()
            if line.startswith(b"$"):
                size = int(line[1:])
                return None if size < 0 else stream.read(size).decode()
            if line.startswith(b"-"):
                raise RuntimeError(line.decode())
            raise RuntimeError(f"unexpected Redis response: {line!r}")


def verify_events(path):
    required = {
        "TestRedisLimiterAtomicHierarchyAndLeaseExpiry",
        "TestHasQueuedQueryApplyForReferenceRedis",
        "TestSnapshotQueuedWorkBoundedRedis",
        "TestAsynqNativeLifecycleRedis",
        "TestAsynqNativePersistenceRedis",
        "TestDocumentStageRepositoryPostgres",
        "TestNoteReadingOptimisticCRUDAndCascadePostgres",
        "TestFailInterruptedWorkPostgres",
    }
    passed = set()
    skipped = []
    for line in path.read_text(encoding="utf-8").splitlines():
        event = json.loads(line)
        test = event.get("Test", "")
        if event.get("Action") == "pass":
            passed.add(test)
        elif event.get("Action") == "skip" and test.endswith(("Redis", "Postgres")):
            skipped.append(test)
    if required - passed or skipped:
        raise RuntimeError(f"integration coverage missing={sorted(required - passed)} skipped={skipped}")
    return sorted(required)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--redis-server", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    binary = args.redis_server.resolve()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    report = {"status": "failed", "steps": []}
    process = None
    try:
        if not os.environ.get("PAPERS_MANAGER_TEST_POSTGRES_DSN"):
            raise RuntimeError("PAPERS_MANAGER_TEST_POSTGRES_DSN must target a private test cluster")
        report["redis_version"] = subprocess.check_output([str(binary), "--version"], text=True).strip()
        with tempfile.TemporaryDirectory(prefix="pm-redis-") as temporary, (output / "redis.log").open("wb") as log:
            with socket.socket() as candidate:
                candidate.bind(("127.0.0.1", 0))
                port = candidate.getsockname()[1]

            def start():
                child = subprocess.Popen([str(binary), "--bind", "127.0.0.1", "--port", str(port),
                                          "--save", "", "--appendonly", "yes", "--appendfsync", "always",
                                          "--maxmemory-policy", "noeviction", "--dir", "."],
                                         cwd=temporary, stdout=log, stderr=subprocess.STDOUT)
                try:
                    deadline = time.monotonic() + 30
                    while time.monotonic() < deadline:
                        if child.poll() is not None:
                            raise RuntimeError(f"Redis exited: {child.returncode}; see redis.log")
                        try:
                            if redis_command(port, "PING") == "PONG":
                                return child
                        except OSError:
                            pass
                        time.sleep(0.1)
                    raise RuntimeError("Redis readiness timed out")
                except BaseException:
                    child.kill()
                    child.wait(timeout=5)
                    raise

            def stop():
                nonlocal process
                if process and process.poll() is None:
                    try:
                        redis_command(port, "SHUTDOWN", "NOSAVE")
                    except (OSError, RuntimeError):
                        pass
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)

            try:
                process = start()
                redis_command(port, "SET", "papers-native-persistence-probe", "persisted")
                env = {**os.environ, "PAPERS_MANAGER_TEST_REDIS_ADDR": f"127.0.0.1:{port}",
                       "PAPERS_TEST_PERSISTENCE_QUEUE": "native-persist-" + secrets.token_hex(12),
                       "PAPERS_TEST_PERSISTENCE_PHASE": "seed"}
                with (output / "go-persistence-seed.log").open("w", encoding="utf-8") as seed_log:
                    subprocess.run([shutil.which("go") or "go", "test", "-count=1", "./internal/router",
                                    "-run", "^TestAsynqNativePersistenceRedis$", "-v"],
                                   env=env, stdout=seed_log, stderr=subprocess.STDOUT, timeout=180, check=True)
                stop()
                process = start()
                if redis_command(port, "GET", "papers-native-persistence-probe") != "persisted":
                    raise RuntimeError("Redis AOF did not survive restart")
                report["steps"].append("Redis AOF restart persisted data")
                env["PAPERS_TEST_PERSISTENCE_PHASE"] = "verify"
                events = output / "go-integration.jsonl"
                with events.open("w", encoding="utf-8") as stdout:
                    completed = subprocess.run([shutil.which("go") or "go", "test", "-json", "-count=1", "./..."],
                                               env=env, stdout=stdout, stderr=subprocess.STDOUT, timeout=900)
                if completed.returncode:
                    # Print failures, keep complete JSON event log as artifact.
                    lines = events.read_text(encoding="utf-8").splitlines()
                    decoded = []
                    for line in lines:
                        try:
                            decoded.append(json.loads(line))
                        except ValueError:
                            print(line)
                    failed_tests = {event.get("Test") for event in decoded
                                    if event.get("Action") == "fail" and event.get("Test")}
                    for line in lines:
                        try:
                            event = json.loads(line)
                            if event.get("Action") == "fail" or event.get("Test") in failed_tests or "FAIL" in event.get("Output", ""):
                                print(line)
                        except ValueError:
                            print(line)
                    raise RuntimeError(f"go test exit={completed.returncode}; see {events}")
                report["required_tests_passed"] = verify_events(events)
                report["steps"].append("Asynq pending/scheduled tasks and crashed worker lease recovered after Redis restart")
                report["status"] = "passed"
            finally:
                stop()
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    (output / "native-integration.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
