#!/usr/bin/env python3
"""Run the real Go recognition workflow against an owned shared Python server."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PARSER = ROOT / "pdf_parser"
sys.path.insert(0, str(PARSER))
import grpc
from papers_parser.worker_transport import stop_process


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    report = {"status": "failed"}
    try:
        with tempfile.TemporaryDirectory(prefix="pm-recognition-key-") as temporary:
            root = Path(temporary)
            pdf = root / "fixture.pdf"
            subprocess.run([sys.executable, "-c",
                            "import pymupdf,sys; d=pymupdf.open(); "
                            "[d.new_page().insert_text((72,72),'Storage key recognition test paper.') for _ in range(2)]; "
                            "d.save(sys.argv[1]); d.close()", str(pdf)], check=True, timeout=30)
            with socket.socket() as candidate:
                candidate.bind(("127.0.0.1", 0))
                address = f"127.0.0.1:{candidate.getsockname()[1]}"
            env = {**os.environ, "PYTHONPATH": str(PARSER), "LOG_PATH": str(output / "parser.log"),
                   "PAPERS_MANAGER_TEST_PARSER_ADDR": address,
                   "PAPERS_MANAGER_TEST_PARSER_UPLOAD_DIR": str(root),
                   "PAPERS_MANAGER_TEST_PARSER_PDF": str(pdf)}
            with (output / "console.log").open("wb") as console:
                process = subprocess.Popen([sys.executable, "-m", "papers_parser.server",
                                            "--address", address, "--upload-dir", str(root),
                                            "--execution-mode", "shared", "--paper-extraction-engine", "rules"],
                                           cwd=PARSER, env=env, stdout=console, stderr=subprocess.STDOUT,
                                           start_new_session=os.name != "nt")
                channel = grpc.insecure_channel(address)
                try:
                    grpc.channel_ready_future(channel).result(timeout=30)
                    path = output / "go-recognition.jsonl"
                    with path.open("w", encoding="utf-8") as events:
                        result = subprocess.run(["go", "test", "-json", "-count=1", "./internal/application/service",
                                                 "-run", "^TestStoredPDFRecognitionParserIntegration$"],
                                                cwd=ROOT, env=env, stdout=events, stderr=subprocess.STDOUT, timeout=180)
                    lines = path.read_text(encoding="utf-8").splitlines()
                    if result.returncode:
                        print("\n".join(lines[-80:]))
                        raise RuntimeError("real recognition workflow failed; see go-recognition.jsonl")
                    passed = {event.get("Test") for line in lines
                              if (event := json.loads(line)).get("Action") == "pass"}
                    expected = {"TestStoredPDFRecognitionParserIntegration",
                                "TestStoredPDFRecognitionParserIntegration/key",
                                "TestStoredPDFRecognitionParserIntegration/bytes-fallback"}
                    if not expected <= passed:
                        raise RuntimeError(f"missing real integration coverage: {expected - passed}")
                    report = {"status": "passed", "tests": sorted(expected),
                              "assertions": ["key success: zero Go source opens, zero PDF bytes in initial RPC",
                                             "invisible shared source: one lazy bytes fallback",
                                             "real shared worker produces Markdown, blocks and two pages",
                                             "actual input mode persisted"]}
                finally:
                    channel.close()
                    stop_process(process)
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    (output / "recognition-key.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
