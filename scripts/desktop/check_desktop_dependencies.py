#!/usr/bin/env python3
"""Record D0 environment, dependency metadata, model and optional ZIP hashes."""
from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import json
import platform
import sys
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--redis-archive", type=Path)
    parser.add_argument("--require-models", action="store_true")
    args = parser.parse_args()
    manifest = json.loads((ROOT / "packaging/desktop/dependencies.json").read_text())
    report = {
        "schema_version": 1, "platform": platform.platform(), "machine": platform.machine(),
        "python": sys.version, "executable": sys.executable,
        "packages": {}, "models": [], "errors": [],
    }
    for name in ("grpcio", "grpcio-tools", "protobuf", "pymupdf", "pymupdf4llm",
                 "pymupdf-layout", "onnxruntime", "numpy", "python-crfsuite"):
        try:
            package = importlib.metadata.metadata(name)
            report["packages"][name] = {
                "version": importlib.metadata.version(name),
                "license": package.get("License-Expression") or package.get("License"),
            }
        except importlib.metadata.PackageNotFoundError:
            report["errors"].append(f"missing package: {name}")
    for model in manifest["models"]:
        path = ROOT / "pdf_parser/paper_crf/weights" / model["file"]
        observed = sha256(path) if path.is_file() else None
        report["models"].append({"file": model["file"], "sha256": observed,
                                 "matches_candidate": observed == model["sha256"]})
        if args.require_models and observed != model["sha256"]:
            report["errors"].append(f"missing or different model: {model['file']}")
    if args.redis_archive:
        observed = sha256(args.redis_archive)
        expected = manifest["windows_redis"]["sha256"]
        if observed != expected:
            report["errors"].append("Windows Redis archive digest differs from pinned candidate")
        with zipfile.ZipFile(args.redis_archive) as archive:
            report["windows_redis"] = {
                "sha256": observed, "matches_candidate": observed == expected,
                "files": [item.filename for item in archive.infolist() if not item.is_dir()],
            }
    report["status"] = "failed" if report["errors"] else "passed"
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"{report['status']}: {args.output}")
    return int(bool(report["errors"]))


if __name__ == "__main__":
    raise SystemExit(main())
