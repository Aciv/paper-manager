"""Shared, fail-closed IO for internal desktop bundles (stdlib only)."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import tarfile
import urllib.request
import zipfile


def sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_json(path: Path, value) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def download(item: dict, cache: Path) -> Path:
    """Cache entries are content-addressed and checked on every use."""
    expected = item["sha256"]
    if len(expected) != 64 or any(c not in "0123456789abcdef" for c in expected):
        raise ValueError("invalid pinned SHA-256")
    if not item["url"].startswith("https://"):
        raise ValueError("dependency source must use HTTPS")
    cache.mkdir(parents=True, exist_ok=True)
    path = cache / (expected + "-" + item["url"].rsplit("/", 1)[-1])
    if path.exists():
        if sha256(path) != expected:
            raise ValueError(f"cached dependency hash mismatch: {path.name}")
        return path
    temporary = path.with_suffix(path.suffix + ".part")
    try:
        request = urllib.request.Request(item["url"], headers={"User-Agent": "papersManager-desktop-build/1.0"})
        with urllib.request.urlopen(request, timeout=120) as response, temporary.open("wb") as out:
            shutil.copyfileobj(response, out, length=1 << 20)
        if sha256(temporary) != expected:
            raise ValueError(f"download hash mismatch: {path.name}")
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)
    return path


def extract(archive: Path, destination: Path, *, prefixes: tuple[str, ...] | None = None) -> None:
    """Accept regular files/directories and safe tar links only."""
    destination.mkdir(parents=True, exist_ok=True)
    if zipfile.is_zipfile(archive):
        with zipfile.ZipFile(archive) as source:
            for item in source.infolist():
                path = PurePosixPath(item.filename.replace("\\", "/"))
                if path.is_absolute() or ".." in path.parts or ":" in str(path):
                    raise ValueError("unsafe ZIP member")
                if (item.external_attr >> 16) & 0o170000 == 0o120000:
                    raise ValueError("ZIP symlinks are not supported")
            selected = source.infolist()
            if prefixes is not None:
                selected = [item for item in selected if any(
                    item.filename == prefix or (prefix.endswith("/") and item.filename.startswith(prefix))
                    for prefix in prefixes
                )]
            source.extractall(destination, members=selected)
    else:
        if prefixes is not None:
            raise ValueError("selective extraction is only supported for ZIP archives")
        with tarfile.open(archive) as source:
            source.extractall(destination, filter="data")


def inventory(root: Path, *, exclude: tuple[str, ...] = ("bundle.json", "SHA256SUMS")) -> dict:
    files = {}
    for path in sorted(root.rglob("*")):
        relative = path.relative_to(root).as_posix()
        if relative in exclude:
            continue
        if path.is_symlink():
            target = path.resolve()
            if not target.is_relative_to(root.resolve()) or not target.exists():
                raise ValueError(f"external or broken bundle symlink: {relative}")
            files[relative] = {"symlink": os.readlink(path)}
        elif path.is_file():
            files[relative] = {"sha256": sha256(path), "bytes": path.stat().st_size}
            if os.name != "nt":
                files[relative]["executable"] = bool(path.stat().st_mode & 0o111)
    return files


def verify(root: Path) -> dict:
    record = json.loads((root / "bundle.json").read_text(encoding="utf-8"))
    if record.get("schema_version") != 1 or record.get("distribution") != "internal-validation-only":
        raise ValueError("unsupported bundle manifest")
    if inventory(root) != record["files"]:
        raise ValueError("bundle files differ from the sealed inventory")
    lines = [f"{item['sha256']}  {name}" for name, item in record["files"].items() if "sha256" in item]
    lines.append(f"{sha256(root / 'bundle.json')}  bundle.json")
    if (root / "SHA256SUMS").read_text(encoding="utf-8") != "\n".join(lines) + "\n":
        raise ValueError("bundle checksum index differs")
    return record
