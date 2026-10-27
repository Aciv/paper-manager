#!/usr/bin/env python3
"""Provision an isolated make dev stack and run the Go business workload."""
import argparse
import json
import os
import platform
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone

import psutil

ROOT = Path(__file__).resolve().parents[1]


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sizes", default="1,10,50,100,250,500,1000")
    parser.add_argument("--concurrency", default="1,4,8,16,32")
    parser.add_argument("--duration", default="8s")
    parser.add_argument("--warmup", default="2s")
    parser.add_argument("--blocks", type=int, default=200)
    parser.add_argument("--sections", type=int, default=12)
    parser.add_argument("--section-bytes", type=int, default=8192)
    parser.add_argument("--pdf-bytes", type=int, default=1048576)
    parser.add_argument("--import-batches", type=int, default=2)
    parser.add_argument("--keep-data", action="store_true", help="retain isolated Docker volumes and fixtures")
    return parser.parse_args()


def free_ports(count):
    held = []
    try:
        for _ in range(count):
            listener = socket.socket()
            listener.bind(("127.0.0.1", 0))
            held.append(listener)
        return [listener.getsockname()[1] for listener in held]
    finally:
        for listener in held:
            listener.close()


def wait_ready(process, url, log):
    # Ignore desktop/system proxy settings for this loopback-only test client.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"make dev exited ({process.returncode}); see {log}")
        try:
            with opener.open(url + "/health", timeout=1) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.5)
    raise TimeoutError(f"API did not become ready; see {log}")


def process_sample(process):
    try:
        with process.oneshot():
            cpu = process.cpu_times()
            return {"pid": process.pid, "rss_mib": process.memory_info().rss / 1048576,
                    "cpu_seconds": cpu.user + cpu.system, "threads": process.num_threads(),
                    "fds": process.num_fds()}
    except (psutil.NoSuchProcess, psutil.AccessDenied):
        return None


def sample_resources(owner, driver, output, stop):
    with output.open("w", encoding="utf-8") as stream:
        while not stop.wait(1):
            record = {"time": datetime.now(timezone.utc).isoformat(), "api": [], "python": [], "driver": []}
            try:
                children = psutil.Process(owner.pid).children(recursive=True)
            except psutil.NoSuchProcess:
                children = []
            for child in children:
                try:
                    name = child.name().lower()
                except psutil.NoSuchProcess:
                    continue
                group = "api" if name == "server" else "python" if "python" in name else None
                if group:
                    value = process_sample(child)
                    if value:
                        record[group].append(value)
            try:
                value = process_sample(psutil.Process(driver.pid)) if driver.poll() is None else None
            except psutil.NoSuchProcess:
                value = None
            if value:
                record["driver"].append(value)
            stream.write(json.dumps(record) + "\n")
            stream.flush()


def stop_services(owner):
    if owner is None:
        return
    try:
        children = psutil.Process(owner.pid).children(recursive=True)
    except psutil.NoSuchProcess:
        children = []
    if owner.poll() is None:
        os.killpg(owner.pid, signal.SIGINT)
        try:
            owner.wait(timeout=35)
        except subprocess.TimeoutExpired:
            pass
    survivors = []
    for child in reversed(children):
        try:
            if child.is_running():
                child.terminate()
                survivors.append(child)
        except psutil.NoSuchProcess:
            pass
    _, alive = psutil.wait_procs(survivors, timeout=5)
    for child in alive:
        try:
            child.kill()
        except psutil.NoSuchProcess:
            pass
    if owner.poll() is None:
        owner.kill()
        owner.wait()


def main():
    args = arguments()
    if sys.platform != "linux":
        raise RuntimeError("This make dev orchestrator currently targets Linux/WSL")
    run_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + f"-{os.getpid()}"
    output = ROOT / "loadtest/results" / run_id
    output.mkdir(parents=True)
    storage = output / "storage"
    storage.mkdir()
    project = "pm-loadtest-" + run_id.lower()
    pg, redis, api, parser, web = free_ports(5)
    env = dict(os.environ)
    env["PATH"] = str(Path(shutil.which("go") or "/usr/local/go/bin/go").parent) + os.pathsep + env["PATH"]
    env.update({
        "COMPOSE_PROJECT_NAME": project, "POSTGRES_PORT": str(pg), "REDIS_PORT": str(redis),
        "HTTP_ADDR": f"127.0.0.1:{api}", "PARSER_GRPC_ADDR": f"127.0.0.1:{parser}",
        "VITE_PORT": str(web), "VITE_HOST": "127.0.0.1",
        "VITE_API_PROXY_TARGET": f"http://127.0.0.1:{api}", "DEV_AUTO_RELOAD": "false",
        "DATABASE_URL": f"postgres://papers:papers@127.0.0.1:{pg}/papers?sslmode=disable",
        "DB_DRIVER": "postgres", "AUTO_MIGRATE": "true", "UPLOAD_DIR": str(storage),
        "REDIS_ADDR": f"127.0.0.1:{redis}", "REDIS_DB": "0", "REDIS_PASSWORD": "", "REDIS_USERNAME": "",
        "DB_MAX_OPEN_CONNS": "48", "DB_MAX_IDLE_CONNS": "24", "ASYNQ_CONCURRENCY": "32",
        "HTTP_WRITE_RATE_PER_MINUTE": "100000", "HTTP_WRITE_RATE_BURST": "100000",
        "HTTP_BATCH_CONCURRENCY": "2", "HTTP_UPLOAD_CONCURRENCY": "4",
        "MCP_ENABLED": "false", "PAPERS_SKIP_DOTENV": "1", "LOG_LEVEL": "info",
        "LOG_PATH": str(output / "application.log"), "LOADTEST_RUN_ID": run_id, "LOADTEST_ISOLATED": "1",
        "SSRF_WHITELIST_EXTRA": "127.0.0.1", "PAPER_CATALOG_TIMEOUT": "1s",
        "PAPER_CATALOG_TASK_TIMEOUT": "5s", "PAPER_CATALOG_TASK_MAX_RETRY": "1",
    })
    metadata = {"run_id": run_id, "project": project, "ports": {"pg": pg, "redis": redis, "api": api,
                "parser": parser, "web": web}, "arguments": vars(args),
                "host": {"cpus": psutil.cpu_count(), "memory_mib": psutil.virtual_memory().total / 1048576,
                         "platform": platform.platform()},
                "go": subprocess.check_output(["go", "version"], env=env, text=True).strip(),
                "revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
                "db_pool": {"open": 48, "idle": 24}, "asynq_concurrency": 32,
                "logging": "info", "http_write_rate_per_minute": 100000,
                "scope": "HTTP business workloads; no LLM clients; core rounds do not call Python"}
    (output / "run.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
    owner = driver = None
    stop = threading.Event()
    sampler = None
    compose = ["docker", "compose", "-p", project, "-f", str(ROOT / "docker-compose.dev.yml")]
    print(f"Results: {output}", flush=True)
    try:
        binary = output / "loadtest"
        subprocess.run(["go", "build", "-o", str(binary), "./loadtest/cmd"], cwd=ROOT, env=env, check=True)
        with (output / "dev.log").open("w") as log:
            owner = subprocess.Popen(["make", "dev"], cwd=ROOT, env=env, stdout=log,
                                     stderr=subprocess.STDOUT, start_new_session=True)
        wait_ready(owner, f"http://127.0.0.1:{api}", output / "dev.log")
        command = [str(binary), "--api", f"http://127.0.0.1:{api}", "--storage", str(storage),
                   "--output", str(output)]
        for name in ("sizes", "concurrency", "duration", "warmup", "blocks", "sections", "section_bytes",
                     "pdf_bytes", "import_batches"):
            command.extend(["--" + name.replace("_", "-"), str(getattr(args, name))])
        with (output / "driver.log").open("w") as log:
            driver = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                      stderr=subprocess.STDOUT, text=True, bufsize=1)
            sampler = threading.Thread(target=sample_resources,
                                       args=(owner, driver, output / "resources.jsonl", stop), daemon=True)
            sampler.start()
            for line in driver.stdout:
                print(line, end="", flush=True)
                log.write(line)
                log.flush()
            code = driver.wait()
        if code:
            raise RuntimeError(f"workload exited {code}; see {output / 'results.json'} and driver.log")
    finally:
        if driver is not None and driver.poll() is None:
            driver.terminate()
            try:
                driver.wait(timeout=20)
            except subprocess.TimeoutExpired:
                driver.kill()
                driver.wait()
        stop.set()
        if sampler:
            sampler.join(timeout=3)
        stop_services(owner)
        result = subprocess.run(compose + ["down", "--remove-orphans"] + ([] if args.keep_data else ["-v"]),
                                cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        (output / "cleanup.log").write_text(result.stdout, encoding="utf-8")
        if result.returncode:
            print(f"Cleanup failed; inspect {output / 'cleanup.log'}", file=sys.stderr)
        elif not args.keep_data:
            shutil.rmtree(storage)
    print(f"Completed: {output / 'summary.md'}", flush=True)


if __name__ == "__main__":
    main()
