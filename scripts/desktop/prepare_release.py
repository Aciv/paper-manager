#!/usr/bin/env python3
"""Select successful tests and verify exact-source user archives for release."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re

from check_desktop_package import verify_checksum

TARGETS = {"linux-x86_64", "windows-x86_64", "macos-arm64"}


def release_notes(notes_dir: Path, version: str) -> str:
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise ValueError("invalid application version")
    content = (notes_dir / f"{version}.md").read_text(encoding="utf-8").strip()
    if not content.startswith(f"# paper_manager {version}\n"):
        raise ValueError("release notes are missing the matching version heading")
    return content + "\n"


def select_test_run(runs: list[dict], revision: str) -> int:
    candidates = [run for run in runs if run.get("head_sha") == revision
                  and run.get("path") == ".github/workflows/test.yml"
                  and run.get("status") == "completed" and run.get("conclusion") == "success"]
    if not candidates:
        raise ValueError("No successful Full tests run for this exact commit. Run Full tests first.")
    return max(run["id"] for run in candidates)


def release_archives(root: Path, revision: str, version: str) -> list[Path]:
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise ValueError("invalid application version")
    result = {}
    for path in root.rglob("*.manifest.json"):
        record = json.loads(path.read_text(encoding="utf-8"))
        target = record.get("target")
        if record.get("distribution") != "user" or target not in TARGETS or target in result:
            raise ValueError("unexpected or duplicate release target")
        if record.get("revision") != revision or record.get("application", {}).get("version") != version:
            raise ValueError("release artifact has stale source or application version")
        suffix = "zip" if target == "windows-x86_64" else "tar.gz"
        archive = path.with_name(f"paper_manager-{version}-{target}.{suffix}")
        if path.name != archive.name + ".manifest.json":
            raise ValueError("artifact name differs from target/version")
        verify_checksum(archive)
        result[target] = archive
    if set(result) != TARGETS:
        raise ValueError("release needs all three tested platform archives")
    return [result[target] for target in sorted(result)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--runs", type=Path)
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--version")
    parser.add_argument("--notes", action="store_true")
    args = parser.parse_args()
    if args.notes and args.version:
        root = Path(__file__).resolve().parents[2]
        print(release_notes(root / "packaging/desktop/releases", args.version), end="")
    elif args.runs:
        payload = json.loads(args.runs.read_text())
        print(select_test_run(payload["workflow_runs"], args.revision))
    elif args.artifacts and args.version:
        for archive in release_archives(args.artifacts, args.revision, args.version):
            print(archive)
    else:
        parser.error("provide --runs or --artifacts with --version")


if __name__ == "__main__":
    main()
