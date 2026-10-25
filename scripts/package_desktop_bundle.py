#!/usr/bin/env python3
"""Archive a sealed internal bundle and its standalone validation tools."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import tarfile
import tempfile

from desktop_bundle import inventory, sha256, verify, write_json

ROOT = Path(__file__).resolve().parents[1]
HARNESS = (
    "check_desktop_launcher.py", "desktop_bundle.py", "check_windows_desktop_user.ps1",
    "benchmark_parser_shared.py", "check_desktop_setup_failures.py", "check_desktop_package.py",
)
REPORTS = ("launcher.json", "setup-failures.json", "parser-benchmark.json")


def package(bundle: Path, output: Path, reports: Path | None = None) -> Path:
    bundle, output = bundle.resolve(), output.resolve()
    checksum = output.with_name(output.name + ".sha256")
    if output.is_relative_to(bundle):
        raise ValueError("archive output must be outside the bundle")
    if output.exists() or checksum.exists():
        raise FileExistsError("archive or checksum already exists; choose another output")
    manifest = verify(bundle)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="desktop-package-", dir=output.parent) as temporary:
        staging = Path(temporary)
        validation = staging / "validation"
        validation.mkdir()
        for name in HARNESS:
            shutil.copy2(ROOT / "scripts" / name, validation / name)
        if reports:
            destination = staging / "reports"
            destination.mkdir()
            for name in REPORTS:
                source = reports / name
                report = json.loads(source.read_text(encoding="utf-8"))
                if report.get("status") != "passed":
                    raise ValueError(f"cannot attach a failed or incomplete report: {name}")
                if name == "launcher.json":
                    recorded = report.get("bundle", {})
                    if any(recorded.get(key) != manifest[key] for key in ("revision", "target", "profile")):
                        raise ValueError("validation report belongs to a different bundle")
                shutil.copy2(source, destination / name)
        record = {
            "schema_version": 1, "distribution": "internal-validation-only",
            "application": manifest.get("application"),
            "revision": manifest["revision"], "target": manifest["target"], "profile": manifest["profile"],
            "bundle_manifest_sha256": sha256(bundle / "bundle.json"),
            "files": inventory(staging, exclude=()),
        }
        write_json(staging / "package.json", record)
        archive = staging / "delivery.tar.gz"
        with tarfile.open(archive, "w:gz", compresslevel=6) as stream:
            stream.add(bundle, arcname="bundle")
            for name in ("validation", "reports", "package.json"):
                if (staging / name).exists():
                    stream.add(staging / name, arcname=name)
        # Reuse the runtime, but never export a directory modified during packing.
        verify(bundle)
        digest = sha256(archive)
        created = False
        checksum_created = False
        complete = False
        try:
            with output.open("xb") as target:
                created = True
                with archive.open("rb") as source:
                    shutil.copyfileobj(source, target, length=1 << 20)
            with checksum.open("x", encoding="utf-8", newline="\n") as target:
                checksum_created = True
                target.write(f"{digest}  {output.name}\n")
            complete = True
        finally:
            if not complete:
                if created:
                    output.unlink(missing_ok=True)
                if checksum_created:
                    checksum.unlink(missing_ok=True)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--reports-dir", type=Path)
    args = parser.parse_args()
    print(package(args.bundle, args.output, args.reports_dir))


if __name__ == "__main__":
    main()
