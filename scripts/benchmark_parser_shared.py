#!/usr/bin/env python3
"""Measure the real shared gRPC server and its workers, never project data.

Run with the parser interpreter. This controller never imports heavy parser
libraries. Reports are local artifacts and may contain executable paths.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import importlib.metadata
import json
import os
import platform
import socket
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import psutil

ROOT = Path(__file__).resolve().parents[1]
PARSER = ROOT / "pdf_parser"
sys.path.insert(0, str(PARSER))

import grpc
import document_parser_pb2 as pb
import document_parser_pb2_grpc as rpc


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, max(0, int(len(ordered) * fraction + 0.999) - 1))]


def snapshot(process: psutil.Process) -> dict:
    parent_rss = process.memory_info().rss
    workers = []
    for child in process.children(recursive=True):
        try:
            command = child.cmdline()
            kind = command[command.index("--kind") + 1] if "--kind" in command else "other"
            workers.append({"pid": child.pid, "kind": kind, "rss": child.memory_info().rss,
                            "threads": child.num_threads()})
        except psutil.NoSuchProcess:
            continue
    return {"parent_rss": parent_rss, "tree_rss": parent_rss + sum(x["rss"] for x in workers),
            "workers": workers}


def stop_owned_process(process: subprocess.Popen) -> None:
    # Snapshot only descendants of the exact process created by this harness.
    try:
        parent = psutil.Process(process.pid)
        descendants = parent.children(recursive=True)
    except psutil.NoSuchProcess:
        descendants = []
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    for child in descendants:
        try:
            child.terminate()
        except psutil.NoSuchProcess:
            pass
    _, remaining = psutil.wait_procs(descendants, timeout=3)
    for child in remaining:
        try:
            child.kill()
        except psutil.NoSuchProcess:
            pass
    psutil.wait_procs(remaining, timeout=3)


def run_case(args, concurrency: int, root: Path) -> dict:
    # Separate cold process for each load level; configured worker budget stays
    # at four. Only the number of simultaneous client requests changes.
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    case = root / str(concurrency)
    case.mkdir()
    pdf = root / "fixture.pdf"
    env = os.environ.copy()
    env.update(PYTHONPATH=str(PARSER), LOG_PATH=str(case / "parser.log"),
               PARSER_LAYOUT_THREADS=str(args.layout_threads),
               PARSER_PYMUPDF_LAYOUT="true", MALLOC_ARENA_MAX="2")
    command = [
        sys.executable, "-m", "papers_parser.server", "--address", f"127.0.0.1:{port}",
        "--upload-dir", str(root), "--execution-mode", "shared",
        "--recognize-concurrency", "4", "--extract-concurrency", "4",
        "--render-concurrency", "4", "--max-workers", "8", "--max-concurrent-rpcs", "8",
        "--worker-idle-seconds", str(args.idle_seconds),
        "--render-worker-idle-seconds", str(args.idle_seconds),
        "--worker-max-jobs", "1000", "--worker-start-timeout", "90",
        "--paper-extraction-engine", args.engine, "--image-dpi", "144",
        "--shutdown-grace-seconds", "5",
    ]
    samples = []
    sampling_errors = []
    stopped = threading.Event()
    with (case / "console.log").open("wb") as log:
        process = subprocess.Popen(command, cwd=PARSER, env=env, stdout=log, stderr=subprocess.STDOUT)
        channel = grpc.insecure_channel(f"127.0.0.1:{port}", options=[
            ("grpc.max_receive_message_length", 105906176),
            ("grpc.max_send_message_length", 105906176),
        ])
        sampler = None
        try:
            start = time.perf_counter()
            grpc.channel_ready_future(channel).result(timeout=30)
            ready_seconds = time.perf_counter() - start
            parent = psutil.Process(process.pid)
            cold = snapshot(parent)
            if cold["workers"]:
                raise RuntimeError("server started heavy workers before first request")

            def sample():
                while not stopped.wait(0.05):
                    try:
                        samples.append(snapshot(parent))
                    except psutil.NoSuchProcess:
                        return
                    except psutil.Error as exc:
                        sampling_errors.append(f"{type(exc).__name__}: {exc}")
                        return

            sampler = threading.Thread(target=sample, daemon=True)
            sampler.start()
            recognizer = rpc.DocumentParserStub(channel)
            extractor = rpc.PaperMetadataExtractorStub(channel)
            renderer = rpc.PdfRendererStub(channel)
            content = pdf.read_bytes()

            def invoke(kind: str):
                started = time.perf_counter()
                document_id = str(uuid.uuid4())
                if kind == "recognize":
                    response = recognizer.Recognize(pb.RecognitionRequest(
                        document_id=document_id, filename="fixture.pdf",
                        content_type="application/pdf", content=content), timeout=90)
                    if response.page_count != args.pages or not response.markdown or not response.blocks:
                        raise RuntimeError("recognition output lost pages/Markdown/blocks")
                elif kind == "extract":
                    response = extractor.Extract(pb.PaperExtractionRequest(
                        document_id=document_id, markdown=b"# Shared Worker Benchmark\n\n"
                        b"Jane Example\n\n## Abstract\nWe compare bounded shared worker concurrency.\n"),
                        timeout=90)
                    if not response.extractor_name:
                        raise RuntimeError("empty extraction response")
                else:
                    response = renderer.RenderPage(pb.PdfRenderRequest(
                        document_id=document_id, source_storage_key="fixture.pdf",
                        page_number=1, full_page=True, dpi=144), timeout=90)
                    if not response.png.startswith(b"\x89PNG"):
                        raise RuntimeError("invalid rendered image")
                return time.perf_counter() - started

            results = {}
            checkpoints = {}
            for kind in ("recognize", "extract", "render"):
                cold_seconds = invoke(kind)
                invoke(kind)  # warm caches without including warmup in throughput
                before_batch = snapshot(parent)
                initial = [worker["pid"] for worker in before_batch["workers"] if worker["kind"] == kind]
                if len(initial) != 1:
                    raise RuntimeError(f"expected one {kind} worker after warmup: {initial}")
                started = time.perf_counter()
                with ThreadPoolExecutor(max_workers=concurrency) as clients:
                    latencies = list(clients.map(lambda _: invoke(kind), range(args.jobs)))
                seconds = time.perf_counter() - started
                checkpoint = snapshot(parent)
                current = [worker["pid"] for worker in checkpoint["workers"] if worker["kind"] == kind]
                if current != initial:
                    raise RuntimeError(f"{kind} worker was replaced or duplicated during batch: {initial} -> {current}")
                checkpoints[kind] = checkpoint
                results[kind] = {
                    "cold_seconds": cold_seconds, "batch_seconds": seconds,
                    "jobs_per_second": args.jobs / seconds,
                    "p50_seconds": statistics.median(latencies),
                    "p95_seconds": percentile(latencies, .95),
                }
            warm = snapshot(parent)
            worker_kinds = sorted(x["kind"] for x in warm["workers"])
            if len(worker_kinds) != len(set(worker_kinds)) or any(
                kind not in ("extract", "recognize", "render") for kind in worker_kinds
            ):
                raise RuntimeError(f"unexpected shared workers: {worker_kinds}")
            # Observe actual idle retirement; keep parent gRPC service alive.
            deadline = time.monotonic() + args.idle_seconds + 15
            while time.monotonic() < deadline:
                idle = snapshot(parent)
                if not idle["workers"]:
                    break
                time.sleep(.2)
            else:
                raise RuntimeError("shared workers failed to retire after idle window")
            if sampling_errors or not samples:
                raise RuntimeError(f"memory sampler did not complete reliably: {sampling_errors}")
            for sample in samples:
                kinds = [x["kind"] for x in sample["workers"]]
                # A freshly forked child can briefly expose the parent's command
                # before exec. Check known worker kinds, and require the exact
                # process identity at each stable per-kind checkpoint above.
                known = [kind for kind in kinds if kind != "other"]
                if len(known) != len(set(known)):
                    raise RuntimeError(f"duplicate/unexpected worker processes observed: {kinds}")
            return {
                "concurrency": concurrency, "ready_seconds": ready_seconds,
                "cold": cold, "warm": warm, "per_kind_checkpoints": checkpoints,
                "idle_after_retirement": idle,
                "peak_tree_rss": max([cold, warm, idle] + samples, key=lambda x: x["tree_rss"])["tree_rss"],
                "peak_parent_rss": max([cold, warm, idle] + samples, key=lambda x: x["parent_rss"])["parent_rss"],
                "metrics": results,
            }
        finally:
            stopped.set()
            if sampler:
                sampler.join(timeout=2)
            channel.close()
            stop_owned_process(process)
            # Preserve diagnostics before TemporaryDirectory removes them.
            for name in ("parser.log", "console.log"):
                source = case / name
                if source.exists():
                    target = args.output.parent / f"{args.output.stem}-c{concurrency}-{name}"
                    target.write_bytes(source.read_bytes())


def compare(current: dict, baseline: dict) -> list[str]:
    # Same environment/fixture/budgets only. RSS sums double-count shared pages;
    # compare this exact sampling method, never Linux RSS to Windows working set.
    if current["fingerprint"] != baseline["fingerprint"]:
        raise ValueError("baseline fingerprint differs; use same host, fixture, versions and configuration")
    failures = []
    for now, old in zip(current["cases"], baseline["cases"], strict=True):
        if now["concurrency"] != old["concurrency"]:
            raise ValueError("baseline load levels differ")
        for kind, metric in now["metrics"].items():
            previous = old["metrics"][kind]
            if metric["jobs_per_second"] < previous["jobs_per_second"] * .8:
                failures.append(f"c{now['concurrency']} {kind}: throughput fell >20%")
            if metric["p95_seconds"] > previous["p95_seconds"] * 1.25 + .02:
                failures.append(f"c{now['concurrency']} {kind}: p95 rose >25% (+20ms noise floor)")
        for field in ("peak_tree_rss", "peak_parent_rss"):
            if now[field] > old[field] * 1.2 + (16 << 20):
                failures.append(f"c{now['concurrency']} {field}: rose >20% (+16MiB noise floor)")
        if now["idle_after_retirement"]["parent_rss"] > old["idle_after_retirement"]["parent_rss"] * 1.2 + (8 << 20):
            failures.append(f"c{now['concurrency']}: idle parent RSS rose >20% (+8MiB noise floor)")
    return failures


def aggregate(reports: list[dict]) -> dict:
    """Take medians of at least three complete runs, preserving their spread."""
    if len(reports) < 3:
        raise ValueError("at least three runs are required for a median baseline")
    first = reports[0]
    if any(report.get("fingerprint") != first["fingerprint"] for report in reports):
        raise ValueError("cannot aggregate different environment/fixture fingerprints")
    if any(report.get("error") or len(report.get("cases", [])) != 2 for report in reports):
        raise ValueError("cannot aggregate incomplete or errored runs")
    result = copy.deepcopy(first)
    result.update(status="passed", regressions=[], aggregate_runs=len(reports), observed_ranges={})
    for index, case in enumerate(result["cases"]):
        originals = [report["cases"][index] for report in reports]
        if any(item["concurrency"] != case["concurrency"] for item in originals):
            raise ValueError("cannot aggregate different load levels")
        for field in ("ready_seconds", "peak_tree_rss", "peak_parent_rss"):
            case[field] = statistics.median(item[field] for item in originals)
        for field in ("parent_rss", "tree_rss"):
            case["idle_after_retirement"][field] = statistics.median(
                item["idle_after_retirement"][field] for item in originals)
        spread = {}
        for kind, metrics in case["metrics"].items():
            spread[kind] = {}
            for metric in metrics:
                values = [item["metrics"][kind][metric] for item in originals]
                metrics[metric] = statistics.median(values)
                spread[kind][metric] = {"min": min(values), "max": max(values)}
        result["observed_ranges"][str(case["concurrency"])] = spread
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--aggregate", nargs="+", type=Path,
                        help="summarize >=3 existing complete runs instead of launching servers")
    parser.add_argument("--pdf", type=Path, help="optional local fixture; --pages must match it")
    parser.add_argument("--jobs", type=int, default=12)
    parser.add_argument("--pages", type=int, default=4)
    parser.add_argument("--idle-seconds", type=float, default=15)
    parser.add_argument("--layout-threads", type=int, default=2)
    parser.add_argument("--engine", choices=("rules", "hybrid", "crf"), default="hybrid")
    args = parser.parse_args()
    if args.jobs < 4 or args.pages < 1 or args.idle_seconds < 5 or args.layout_threads < 1:
        parser.error("need jobs>=4, pages>=1, idle-seconds>=5 and layout-threads>=1")
    args.output = args.output.resolve()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    if args.aggregate:
        report = aggregate([json.loads(path.read_text(encoding="utf-8")) for path in args.aggregate])
        report["regressions"] = compare(report, json.loads(args.baseline.read_text(encoding="utf-8"))) if args.baseline else []
        report["status"] = "failed" if report["regressions"] else "passed"
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"{report['status']}: {args.output}", flush=True)
        return int(bool(report["regressions"]))
    report = {"schema_version": 1, "status": "running", "cases": []}
    exit_code = 0
    try:
        with tempfile.TemporaryDirectory(prefix="pm-shared-bench-") as temporary:
            root = Path(temporary)
            target = root / "fixture.pdf"
            if args.pdf:
                target.write_bytes(args.pdf.read_bytes())
            else:
                # Fixture generation happens in another process so measurements
                # cannot hide heavy imports in the controller or server.
                subprocess.run([sys.executable, "-c",
                    "import pymupdf,sys; d=pymupdf.open(); "
                    "[(lambda p: [p.insert_text((50,60+i*18),"
                    "'Shared worker benchmark paper, method and results line '+str(i))"
                    " for i in range(25)])(d.new_page()) for _ in range(int(sys.argv[2]))]; "
                    "d.save(sys.argv[1],no_new_id=True); d.close()", str(target), str(args.pages)], check=True, timeout=30)
            report["fingerprint"] = {
                "platform": platform.platform(), "machine": platform.machine(),
                "python": platform.python_version(), "cpu_count": os.cpu_count(),
                "host": platform.node(), "jobs": args.jobs, "pages": args.pages,
                "idle_seconds": args.idle_seconds, "layout_threads": args.layout_threads,
                "engine": args.engine, "pdf_sha256": hashlib.sha256(target.read_bytes()).hexdigest(),
                "versions": {name: importlib.metadata.version(name) for name in (
                    "grpcio", "protobuf", "pymupdf", "pymupdf4llm", "pymupdf-layout",
                    "onnxruntime", "python-crfsuite", "psutil")},
                "model_sha256": {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                                 for path in sorted((PARSER / "paper_crf" / "weights").glob("*.crfsuite"))},
            }
            for concurrency in (1, 4):
                print(f"benchmark shared server load={concurrency}", flush=True)
                report["cases"].append(run_case(args, concurrency, root))
        report["regressions"] = compare(report, json.loads(args.baseline.read_text())) if args.baseline else []
        report["status"] = "failed" if report["regressions"] else "passed"
        exit_code = int(bool(report["regressions"]))
    except Exception as exc:
        report.update(status="failed", error=f"{type(exc).__name__}: {exc}")
        exit_code = 1
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"{report['status']}: {args.output}", flush=True)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
