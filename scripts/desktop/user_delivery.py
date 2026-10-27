"""Small user-facing archives; validation tools remain outside the delivery."""
from __future__ import annotations

import base64
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import zipfile

from desktop_bundle import inventory, sha256, verify

ROOT = Path(__file__).resolve().parents[2]


def windows_uninstall_command(script: str) -> str:
    """Release CMD's batch context before PowerShell deletes the entry file."""
    encoded = base64.b64encode(script.encode("utf-16le")).decode("ascii")
    return (
        '@echo off\r\nset "PM_UNINSTALL_ROOT=%~dp0"\r\n'
        # A command group or trailing exit still resumes the deleted batch.
        # The no-target goto ends that context; the remaining native command
        # runs synchronously and supplies the final process exit status.
        '(goto) 2>nul & '
        f'"%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe" '
        f'-NoProfile -ExecutionPolicy Bypass -EncodedCommand {encoded}\r\n'
    )


def uninstall_entry(target: str) -> tuple[str, str]:
    if target == "windows-x86_64":
        # The helper runs outside the program directory so Windows can remove
        # the original executable. No system Python or installer is involved.
        script = """$ErrorActionPreference = 'Stop'
$root = $env:PM_UNINSTALL_ROOT.TrimEnd('\\')
Write-Host ('Remove program files from ' + $root + '? User data will be kept.')
if ((Read-Host 'Exit Launcher first. Type REMOVE to continue') -cne 'REMOVE') { exit 0 }
$temporary = Join-Path ([IO.Path]::GetTempPath()) ('paper-manager-remove-' + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
  $helper = Join-Path $temporary 'remove.exe'
  Copy-Item -LiteralPath (Join-Path $root 'paper_manager.exe') -Destination $helper
  Set-Location $temporary
  $arguments = @('--uninstall-dir', ('"' + $root + '"'))
  $process = Start-Process -FilePath $helper -ArgumentList $arguments -Wait -PassThru -NoNewWindow
  $code = $process.ExitCode
} finally {
  Set-Location ([IO.Path]::GetTempPath())
  Remove-Item -LiteralPath $temporary -Recurse -Force
}
if ($code -eq 0) { Write-Host 'Program removed. User data kept.' }
Read-Host 'Press Enter to close' | Out-Null
exit $code
"""
        return "Uninstall.cmd", windows_uninstall_command(script)
    name = "Uninstall.command" if target == "macos-arm64" else "Uninstall.sh"
    return name, """#!/bin/sh
set -eu
program=$(CDPATH= cd -- "$(/usr/bin/dirname -- "$0")" && pwd -P)
printf 'Remove program files from %s? User data will be kept.\\n' "$program"
printf 'Exit Launcher first. Type REMOVE to continue: '
IFS= read -r answer || exit 0
[ "$answer" = REMOVE ] || exit 0
temporary=$(/usr/bin/mktemp -d "${TMPDIR:-/tmp}/paper-manager-remove.XXXXXX")
trap '/bin/rm -f "$temporary/remove"; /bin/rmdir "$temporary"' EXIT
/bin/cp "$program/paper_manager" "$temporary/remove"
cd /
"$temporary/remove" --uninstall-dir "$program"
printf 'Program removed. User data kept.\\n'
"""


def package_user(bundle: Path, output: Path) -> Path:
    bundle, output = bundle.resolve(), output.resolve()
    sidecars = [output.with_name(output.name + suffix) for suffix in (".sha256", ".manifest.json")]
    if output.is_relative_to(bundle):
        raise ValueError("archive output must be outside the bundle")
    if any(path.exists() for path in [output, *sidecars]):
        raise FileExistsError("archive or sidecar exists; choose a new output")
    manifest = verify(bundle)
    if manifest.get("unpacked_uninstall") != 1:
        raise ValueError("rebuild the native launcher for user delivery with uninstall support")
    for name in ("papers-manager-library", "papers-manager-reading"):
        if not (bundle / "skills" / name / "SKILL.md").is_file():
            raise ValueError("rebuild the bundle with the shipped skills")
    target = manifest["target"]
    if target not in {"linux-x86_64", "windows-x86_64", "macos-arm64"}:
        raise ValueError("unsupported user delivery target")
    windows = target == "windows-x86_64"
    if output.suffix.lower() == ".zip" and not windows:
        raise ValueError("use tar.gz to preserve Unix permissions and links")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="user-package-", dir=output.parent) as temporary:
        staging = Path(temporary)
        entry = "paper_manager.exe" if windows else "paper_manager"
        shutil.copy2(bundle / ("launcher.exe" if windows else "launcher"), staging / entry)
        (staging / entry).chmod(0o755)
        name, script = uninstall_entry(target)
        (staging / name).write_bytes(script.encode("utf-8"))
        (staging / name).chmod(0o755)
        shutil.copy2(ROOT / "packaging/desktop/USER.md", staging / "README.md")
        shutil.copytree(bundle / "skills", staging / "skills")
        (staging / ".launcher_env").write_text(
            "# Optional Launcher overrides. Restart Launcher after editing.\n"
            "# Ports: launcher.json. Concurrency: management page > Configuration.\n"
            "# Default data directory: data/ beside this file.\n"
            "# LAUNCHER_OPEN_BROWSER=false\n", encoding="utf-8",
        )
        (staging / "launcher.json").write_text(
            json.dumps({"ports": {
                "manager": 28450, "api": 28451, "mcp": 28452,
                "postgres": 28453, "redis": 28454, "parser": 28455,
            }}, indent=2) + "\n", encoding="utf-8",
        )
        record = {
            "schema_version": 1, "distribution": "user", "target": target,
            "application": manifest.get("application"), "revision": manifest["revision"],
            "profile": manifest["profile"], "entrypoint": entry, "uninstaller": name,
            "bundle_manifest_sha256": sha256(bundle / "bundle.json"),
            "files": inventory(staging, exclude=()),
        }
        sources = [(bundle, "bundle"), *((path, path.name) for path in sorted(staging.iterdir()))]
        archive = staging / "archive"
        if output.suffix.lower() == ".zip":
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as stream:
                for source, prefix in sources:
                    for path in sorted(source.rglob("*")) if source.is_dir() else [source]:
                        if path.is_symlink():
                            raise ValueError("Windows ZIP must not contain symlinks")
                        if path.is_file():
                            tail = path.relative_to(source) if source.is_dir() else Path()
                            stream.write(path, (Path(prefix) / tail).as_posix())
        else:
            with tarfile.open(archive, "w:gz", compresslevel=6) as stream:
                for source, prefix in sources:
                    stream.add(source, arcname=prefix)
        verify(bundle)
        created = []
        try:
            with output.open("xb") as destination, archive.open("rb") as source:
                created.append(output)
                shutil.copyfileobj(source, destination)
            with sidecars[0].open("x", encoding="utf-8", newline="\n") as stream:
                created.append(sidecars[0])
                stream.write(f"{sha256(output)}  {output.name}\n")
            # Records live beside the archive, never in the user's unpacked folder.
            with sidecars[1].open("x", encoding="utf-8") as stream:
                created.append(sidecars[1])
                json.dump(record, stream, ensure_ascii=False, indent=2)
                stream.write("\n")
        except BaseException:
            for path in reversed(created):
                path.unlink(missing_ok=True)
            raise
    return output
