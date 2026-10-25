#!/usr/bin/env python3
"""Verify or exercise an exported desktop archive without a source checkout."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

# This entry point may itself live inside the sealed validation directory.
# Disable bytecode before importing its hashed sibling module.
sys.dont_write_bytecode = True

from desktop_bundle import extract, inventory, sha256, verify


def verify_delivery(root: Path) -> dict:
    record = json.loads((root / "package.json").read_text(encoding="utf-8"))
    if record.get("schema_version") != 1 or record.get("distribution") != "internal-validation-only":
        raise ValueError("unsupported delivery manifest")
    allowed = {"bundle", "validation", "reports", "package.json"}
    if any(path.name not in allowed for path in root.iterdir()):
        raise ValueError("unexpected delivery contents")
    manifest = verify(root / "bundle")
    if record["bundle_manifest_sha256"] != sha256(root / "bundle/bundle.json"):
        raise ValueError("delivery points to a different bundle manifest")
    if any(record[key] != manifest[key] for key in ("revision", "target", "profile")):
        raise ValueError("delivery identity differs from the bundle")
    if record.get("application") != manifest.get("application"):
        raise ValueError("delivery product metadata differs from the bundle")
    observed = {}
    for name in ("validation", "reports"):
        observed.update({name + "/" + key: value for key, value in inventory(root / name, exclude=()).items()})
    if observed != record["files"]:
        raise ValueError("validation tools or attached reports differ from their sealed inventory")
    return record


def verify_checksum(archive: Path) -> None:
    checksum = archive.with_name(archive.name + ".sha256")
    expected = f"{sha256(archive)}  {archive.name}\n"
    if checksum.read_text(encoding="utf-8") != expected:
        raise ValueError("archive checksum differs")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--archive", type=Path)
    source.add_argument("--directory", type=Path, help="already extracted delivery directory")
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--verify-only", action="store_true")
    action.add_argument("--output-dir", type=Path, help="run the full ordinary-user acceptance probe")
    args = parser.parse_args()
    output = args.output_dir.resolve() if args.output_dir else None
    with tempfile.TemporaryDirectory(prefix="pm delivery 阅读 ") as temporary:
        if args.archive:
            archive = args.archive.resolve()
            verify_checksum(archive)
            root = Path(temporary)
            extract(archive, root)
        else:
            root = args.directory.resolve()
        record = verify_delivery(root)
        if output:
            subprocess.run([
                sys.executable, str(root / "validation/check_desktop_launcher.py"),
                "--bundle", str(root / "bundle"), "--output-dir", str(output),
            ], check=True, env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"})
        print(f"passed: {record['target']} / {record['profile']} / {record['revision']}")


if __name__ == "__main__":
    main()
