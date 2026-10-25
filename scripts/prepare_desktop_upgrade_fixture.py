#!/usr/bin/env python3
"""Local tests only: reuse sealed native dependencies and overlay current code.

The normal release builder remains build_desktop_bundle.py. This helper avoids
recompiling unchanged PostgreSQL/Redis while iterating on the offline updater.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import shutil

from desktop_bundle import inventory, sha256, verify, write_json

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    base, output = args.base.resolve(), args.output.resolve()
    manifest = verify(base)
    expected = {"Linux": "linux-x86_64", "Windows": "windows-x86_64", "Darwin": "macos-arm64"}[platform.system()]
    if output.is_relative_to(base):
        raise ValueError("fixture output must be outside the base bundle")
    if manifest["target"] != expected or output.exists():
        raise ValueError("fixture needs a native base and a new output directory")
    if not (ROOT / "web/dist/index.html").is_file():
        raise ValueError("build the current frontend before preparing a fixture")
    shutil.copytree(base, output, symlinks=True)
    suffix = ".exe" if os.name == "nt" else ""
    for name in ("launcher", "server"):
        shutil.copy2(args.bin_dir / (name + suffix), output / (name + suffix))
    for name in ("papers_parser", "paper_crf"):
        destination = output / "pdf_parser" / name
        # Preserve the verified model weights, replace code only.
        saved = None
        if (destination / "weights").exists():
            saved = output / "fixture-weights"
            (destination / "weights").rename(saved)
        shutil.rmtree(destination)
        shutil.copytree(ROOT / "pdf_parser" / name, destination,
                        ignore=shutil.ignore_patterns("__pycache__", "test_*", "*.pyc", "weights"))
        if saved:
            saved.rename(destination / "weights")
    for path in (ROOT / "pdf_parser").glob("*.py"):
        shutil.copy2(path, output / "pdf_parser" / path.name)
    shutil.rmtree(output / "web")
    shutil.copytree(ROOT / "web/dist", output / "web")
    for name in ("THIRD_PARTY_NOTICES.md", "dependencies.json", "sources.lock.json"):
        target = "dependency-audit.json" if name == "dependencies.json" else name
        shutil.copy2(ROOT / "packaging/desktop" / name, output / target)
    shutil.copy2(ROOT / "LICENSE", output / "licenses/paper_manager-MIT.txt")
    shutil.copy2(ROOT / "packaging/desktop/BUNDLE.md", output / "README.md")
    if os.name == "nt":
        for name in ("cyggcc_s-seh-1.dll", "cygiconv-2.dll", "cygintl-8.dll", "cygstdc++-6.dll"):
            (output / "redis" / name).unlink(missing_ok=True)
    manifest["application"] = json.loads((ROOT / "packaging/desktop/product.json").read_text())
    manifest["compatibility"] = {"postgres_major": 17, "data_format": 1, "task_payload": 1}
    manifest["revision"] = "local-working-tree-d4-fixture"
    manifest["fixture_only"] = True
    manifest["launcher_layout"] = "nested-bundle-v1"
    manifest["reused_runtime_manifest"] = sha256(base / "bundle.json")
    manifest["files"] = inventory(output)
    write_json(output / "bundle.json", manifest)
    checks = [f"{item['sha256']}  {name}" for name, item in manifest["files"].items() if "sha256" in item]
    checks.append(f"{sha256(output / 'bundle.json')}  bundle.json")
    (output / "SHA256SUMS").write_text("\n".join(checks) + "\n", encoding="utf-8")
    verify(output)
    print(f"Prepared test-only working-tree fixture: {output}")


if __name__ == "__main__":
    main()
