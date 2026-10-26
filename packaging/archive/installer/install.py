#!/usr/bin/env python3
"""Offline per-user installer, executed with the bundle's private Python."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import sys


def default_paths(*, include_shortcut: bool = False) -> tuple[Path, Path, Path | None]:
    home = Path.home()
    if sys.platform == "win32":
        local = Path(os.environ["LOCALAPPDATA"])
        data = Path(os.environ["APPDATA"])
        desktop = None
        if include_shortcut:
            powershell = Path(os.environ["SYSTEMROOT"]) / "System32/WindowsPowerShell/v1.0/powershell.exe"
            try:
                value = subprocess.check_output([
                    str(powershell), "-NoProfile", "-NonInteractive", "-Command",
                    "[Console]::OutputEncoding = [Text.Encoding]::UTF8; [Environment]::GetFolderPath('Desktop')",
                ], encoding="utf-8", timeout=10).strip()
                if value:
                    desktop = Path(value)
                else:
                    print("Warning: desktop path unavailable; shortcut skipped.", file=sys.stderr)
            except (OSError, subprocess.SubprocessError):
                print("Warning: desktop path lookup failed; shortcut skipped.", file=sys.stderr)
        return local / "paper_manager-program", data / "papers-manager", desktop
    if sys.platform == "darwin":
        data = home / "Library/Application Support"
        return data / "paper_manager-program", data / "papers-manager", home / "Applications"
    data = Path(os.environ.get("XDG_DATA_HOME", home / ".local/share"))
    return data / "paper_manager-program", data / "papers-manager", data / "applications"


def validate_install_paths(program: Path, data: Path) -> None:
    """Managed Windows installs require ASCII paths; do not guess 8.3 support."""
    if sys.platform == "win32" and not (str(program).isascii() and str(data).isascii()):
        raise ValueError(
            "Windows 安装目录和数据目录暂不支持中文或其他非 ASCII 字符。"
            "请选择英文路径，例如 --program-dir C:/paper_manager "
            "--data-dir C:/paper_manager_data。现有文件不会被移动或删除。"
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("install", "uninstall", "retry", "cleanup", "status",
                                             "restore", "delete-checkpoint"))
    parser.add_argument("--program-dir", type=Path)
    parser.add_argument("--data-dir", type=Path)
    parser.add_argument("--create-shortcut", action="store_true",
                        help="opt in to creating a desktop/application shortcut")
    parser.add_argument("--shortcut-dir", type=Path, help="create a shortcut in this directory (explicit opt-in)")
    parser.add_argument("--checkpoint-id")
    parser.add_argument("--confirm-restore", action="store_true")
    parser.add_argument("--confirm-delete", action="store_true")
    parser.add_argument("--no-launch", action="store_true")
    args = parser.parse_args()
    program, data, default_shortcuts = default_paths(
        include_shortcut=args.create_shortcut and args.shortcut_dir is None
    )
    program = (args.program_dir or program).resolve()
    data = (args.data_dir or data).resolve()
    shortcuts = args.shortcut_dir or (default_shortcuts if args.create_shortcut else None)
    if shortcuts is not None:
        shortcuts = shortcuts.resolve()
    if args.operation in ("install", "retry"):
        validate_install_paths(program, data)
    root = Path(__file__).resolve().parents[1]
    bundle = root / "bundle"
    suffix = ".exe" if sys.platform == "win32" else ""
    command = [str(bundle / ("launcher" + suffix)), "--maintenance", args.operation,
               "--program-dir", str(program), "--data-dir", str(data),
               "--source-bundle", str(bundle)]
    if shortcuts is not None:
        command += ["--shortcut-dir", str(shortcuts)]
    if args.checkpoint_id:
        command += ["--checkpoint-id", args.checkpoint_id]
    if args.confirm_restore:
        command.append("--confirm-restore")
    if args.confirm_delete:
        command.append("--confirm-delete")
    print(f"paper_manager · {platform.system()} · {args.operation}", flush=True)
    print("Exit the existing Launcher before updating, restoring or uninstalling.", flush=True)
    subprocess.run(command, check=True)
    if args.operation in ("install", "retry"):
        record = json.loads((program / "installation.json").read_text(encoding="utf-8"))
        key = record["active"]
        if not key or any(character not in "abcdefghijklmnopqrstuvwxyz0123456789.-" for character in key):
            raise ValueError("invalid installed version reference")
        print(f"Installed: {program}\nUser data: {data}", flush=True)
        if not args.no_launch:
            installed = program / "versions" / key
            subprocess.Popen([str(installed / ("launcher" + suffix)), "--data-dir", str(data),
                              "--install-dir", str(installed)], cwd=program)
    elif args.operation == "uninstall":
        print(f"Program removed. User data and checkpoints preserved: {data}", flush=True)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"Installer failed: {error}", file=sys.stderr)
        raise SystemExit(1)
