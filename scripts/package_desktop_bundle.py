#!/usr/bin/env python3
"""Archive a sealed internal bundle and its standalone validation tools."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import zipfile

from desktop_bundle import inventory, sha256, verify, write_json

ROOT = Path(__file__).resolve().parents[1]
HARNESS = (
    "check_desktop_launcher.py", "desktop_bundle.py", "check_windows_desktop_user.ps1",
    "benchmark_parser_shared.py", "check_desktop_setup_failures.py", "check_desktop_package.py",
    "check_desktop_upgrade.py",
)
REPORTS = ("launcher.json", "setup-failures.json", "parser-benchmark.json")
DELIVERY_DOCS = ("README.md", "INSTALL.md", "THIRD_PARTY_NOTICES.md", "VALIDATION.md", "LICENSE")
BOOTSTRAPS = ("Install.sh", "Uninstall.sh", "Install.cmd", "Uninstall.cmd", "Install.command", "Uninstall.command")


def package(
    bundle: Path,
    output: Path,
    reports: Path | None = None,
    failure_probe: Path | None = None,
    *,
    layout: str = "unpacked",
) -> Path:
    bundle, output = bundle.resolve(), output.resolve()
    checksum = output.with_name(output.name + ".sha256")
    if output.is_relative_to(bundle):
        raise ValueError("archive output must be outside the bundle")
    if output.exists() or checksum.exists():
        raise FileExistsError("archive or checksum already exists; choose another output")
    manifest = verify(bundle)
    if layout not in ("unpacked", "maintenance"):
        raise ValueError("unsupported delivery layout")
    if layout == "unpacked" and manifest.get("launcher_layout") != "nested-bundle-v1":
        raise ValueError(
            "rebuild the launcher for unpacked delivery, "
            "or use --layout maintenance for an old bundle"
        )
    is_zip = output.suffix.lower() == ".zip"
    if is_zip and manifest["target"] != "windows-x86_64":
        raise ValueError(
            "ZIP delivery is for Windows; "
            "use tar.gz to preserve Unix links and executable permissions"
        )
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="desktop-package-", dir=output.parent) as temporary:
        staging = Path(temporary)
        entrypoint = "paper_manager.exe" if manifest["target"] == "windows-x86_64" else "paper_manager"
        launcher = bundle / ("launcher.exe" if manifest["target"] == "windows-x86_64" else "launcher")
        if not launcher.is_file() or launcher.is_symlink():
            raise ValueError("bundle must contain a native launcher executable")
        if layout == "unpacked":
            shutil.copy2(launcher, staging / entrypoint)
            (staging / entrypoint).chmod(0o755)
        validation = staging / "validation"
        validation.mkdir()
        for name in HARNESS:
            shutil.copy2(ROOT / "scripts" / name, validation / name)
        if failure_probe is not None:
            if not failure_probe.is_file() or failure_probe.is_symlink():
                raise ValueError("failure probe must be a native regular executable")
            suffix = ".exe" if manifest["target"] == "windows-x86_64" else ""
            shutil.copy2(failure_probe, validation / ("upgrade-failure" + suffix))
        installer = staging / "installer"
        installer.mkdir()
        shutil.copy2(ROOT / "packaging/desktop/installer/install.py", installer / "install.py")
        for name in BOOTSTRAPS:
            source_name = name.replace(".command", ".sh")
            shutil.copy2(ROOT / "packaging/desktop/installer" / source_name, staging / name)
            if name.endswith((".sh", ".command")):
                (staging / name).chmod(0o755)
        for name in DELIVERY_DOCS:
            if name == "LICENSE":
                source = ROOT / "LICENSE"
            else:
                source = ROOT / "packaging/desktop" / ("INSTALL.md" if name == "README.md" else name)
            shutil.copy2(source, staging / name)
        if layout == "maintenance":
            guide = staging / "README.md"
            legacy = (
                "# Legacy maintenance export\n\n"
                "This archive has no root paper_manager executable. Use the optional "
                "Install entry or bundle/launcher (bundle/launcher.exe on Windows). "
                "The unpacked root-entry instructions below require a rebuilt bundle.\n\n"
            )
            guide.write_text(legacy + guide.read_text(encoding="utf-8"), encoding="utf-8")
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
        if layout == "unpacked":
            record.update(entrypoint=entrypoint, launch_mode="unpacked")
        write_json(staging / "package.json", record)
        names = ("validation", "reports", "installer", "package.json", *BOOTSTRAPS, *DELIVERY_DOCS)
        if layout == "unpacked":
            names += (entrypoint,)
        archive = staging / ("delivery.zip" if is_zip else "delivery.tar.gz")
        if is_zip:
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as stream:
                for source, prefix in [(bundle, "bundle"), *((staging / name, name) for name in names)]:
                    paths = sorted(source.rglob("*")) if source.is_dir() else [source]
                    for path in paths:
                        if path.is_symlink():
                            raise ValueError("Windows ZIP delivery must not contain symlinks")
                        if path.is_file():
                            relative = path.relative_to(source) if source.is_dir() else Path()
                            stream.write(path, (Path(prefix) / relative).as_posix())
        else:
            with tarfile.open(archive, "w:gz", compresslevel=6) as stream:
                stream.add(bundle, arcname="bundle")
                for name in names:
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
    parser.add_argument("--failure-probe", type=Path, help="validation-only native synthetic migration failure tool")
    parser.add_argument("--layout", choices=("unpacked", "maintenance"), default="unpacked")
    args = parser.parse_args()
    print(package(args.bundle, args.output, args.reports_dir, args.failure_probe, layout=args.layout))


if __name__ == "__main__":
    main()
