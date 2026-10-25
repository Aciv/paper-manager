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

DELIVERY_DOCS = ("README.md", "INSTALL.md", "THIRD_PARTY_NOTICES.md", "VALIDATION.md", "LICENSE")
BOOTSTRAPS = ("Install.sh", "Uninstall.sh", "Install.cmd", "Uninstall.cmd", "Install.command", "Uninstall.command")
ENTRYPOINTS = ("paper_manager", "paper_manager.exe")


def verify_delivery(root: Path) -> dict:
    record = json.loads((root / "package.json").read_text(encoding="utf-8"))
    if record.get("schema_version") != 1 or record.get("distribution") != "internal-validation-only":
        raise ValueError("unsupported delivery manifest")
    allowed = {
        "bundle", "validation", "reports", "installer", "package.json",
        *BOOTSTRAPS, *DELIVERY_DOCS, *ENTRYPOINTS,
    }
    if any(path.name not in allowed for path in root.iterdir()):
        raise ValueError("unexpected delivery contents")
    manifest = verify(root / "bundle")
    if record["bundle_manifest_sha256"] != sha256(root / "bundle/bundle.json"):
        raise ValueError("delivery points to a different bundle manifest")
    if any(record[key] != manifest[key] for key in ("revision", "target", "profile")):
        raise ValueError("delivery identity differs from the bundle")
    if record.get("application") != manifest.get("application"):
        raise ValueError("delivery product metadata differs from the bundle")
    entrypoint = record.get("entrypoint")
    if entrypoint is not None:
        expected = "paper_manager.exe" if manifest["target"] == "windows-x86_64" else "paper_manager"
        if entrypoint != expected or record.get("launch_mode") != "unpacked":
            raise ValueError("invalid unpacked launcher entry point")
        path = root / entrypoint
        native = root / "bundle" / ("launcher.exe" if expected.endswith(".exe") else "launcher")
        if not path.is_file() or path.is_symlink() or sha256(path) != sha256(native):
            raise ValueError("root launcher differs from the sealed runtime executable")
    elif any((root / name).exists() for name in ENTRYPOINTS):
        raise ValueError("unrecorded root launcher")
    observed = {}
    for name in ("validation", "reports", "installer"):
        observed.update({name + "/" + key: value for key, value in inventory(root / name, exclude=()).items()})
    for name in (*BOOTSTRAPS, *DELIVERY_DOCS, *ENTRYPOINTS):
        path = root / name
        if path.exists():
            if not path.is_file() or path.is_symlink():
                raise ValueError("installer entry point must be a regular file")
            item = {"sha256": sha256(path), "bytes": path.stat().st_size}
            if os.name != "nt":
                item["executable"] = bool(path.stat().st_mode & 0o111)
            observed[name] = item
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
    parser.add_argument("--upgrade", action="store_true", help="run D4 install/upgrade/restore acceptance instead")
    args = parser.parse_args()
    if args.upgrade and not args.output_dir:
        parser.error("--upgrade requires --output-dir")
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
            probe = "check_desktop_upgrade.py" if args.upgrade else "check_desktop_launcher.py"
            subprocess.run([
                sys.executable, str(root / "validation" / probe),
                "--bundle", str(root / "bundle"), "--output-dir", str(output),
            ], check=True, env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"})
        print(f"passed: {record['target']} / {record['profile']} / {record['revision']}")


if __name__ == "__main__":
    main()
