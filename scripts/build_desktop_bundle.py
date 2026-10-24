#!/usr/bin/env python3
"""Build a native internal-only directory bundle; never publish or install globally."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

from desktop_bundle import download, extract, inventory, sha256, verify, write_json

ROOT = Path(__file__).resolve().parents[1]


def run(*command, cwd=None, env=None):
    subprocess.run([str(x) for x in command], cwd=cwd, env=env, check=True)


def copy_notices(source: Path, destination: Path):
    """Preserve supplied notices; this collection is not a legal approval."""
    for path in source.rglob("*"):
        if path.is_file() and any(word in path.name.lower() for word in ("license", "copying", "notice")):
            target = destination / path.relative_to(source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)


def python_inventory(python: Path, output: Path):
    # Only metadata is imported: the lightweight parent remains free of heavy imports.
    code = (
        "import importlib.metadata as m,json;"
        "print(json.dumps([{'name':d.metadata['Name'],'version':d.version,"
        "'license':d.metadata.get('License-Expression') or d.metadata.get('License','UNKNOWN')}"
        " for d in m.distributions()]))"
    )
    packages = json.loads(subprocess.check_output([str(python), "-I", "-c", code], text=True))
    write_json(output, packages)
    return packages


def relocate_macos_libraries(postgres: Path):
    # PG's build prefix is temporary. Replace each private dylib reference with
    # a loader-relative path before moving the directory to its final location.
    binaries = list((postgres / "bin").iterdir()) + list((postgres / "lib").glob("*.dylib"))
    for binary in binaries:
        if binary.is_symlink() or not binary.is_file():
            continue
        dependencies = subprocess.check_output(["otool", "-L", str(binary)], text=True)
        if binary.suffix == ".dylib":
            run("install_name_tool", "-id", "@rpath/" + binary.name, binary)
        for line in dependencies.splitlines()[1:]:
            dependency = line.strip().split(" (", 1)[0]
            if dependency.startswith(str(postgres)):
                relative = os.path.relpath(dependency, binary.parent)
                run("install_name_tool", "-change", dependency, "@loader_path/" + relative, binary)
        run("codesign", "--force", "--sign", "-", binary)


def assemble(args):
    machine = platform.machine().lower()
    target = {("Linux", "x86_64"): "linux-x86_64", ("Windows", "amd64"): "windows-x86_64",
              ("Darwin", "arm64"): "macos-arm64"}.get((platform.system(), machine))
    if not target:
        raise RuntimeError(f"unsupported native build target: {platform.system()}/{machine}")
    lock = json.loads((ROOT / "packaging/desktop/sources.lock.json").read_text())
    # Fail on missing/corrupt inputs before spending time compiling databases.
    if args.output.exists():
        raise RuntimeError("output already exists; use a new version directory")
    archives = {"python": download(lock["python"][target], args.cache)}
    for name in (("postgres_windows", "redis_windows") if os.name == "nt"
                 else ("postgres_source", "redis_source")):
        archives[name] = download(lock[name], args.cache)
    output = args.output.resolve()
    if output.exists():
        raise RuntimeError("output already exists; use a new version directory")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="desktop-build-", dir=output.parent) as temporary:
        work = Path(temporary)
        bundle = work / "bundle"
        bundle.mkdir()
        suffix = ".exe" if os.name == "nt" else ""
        build_env = {**os.environ, "CGO_ENABLED": "0"}
        for binary in ("launcher", "server"):
            run(args.go, "build", "-trimpath", "-o", bundle / (binary + suffix),
                f"./cmd/{binary}", cwd=ROOT, env=build_env)
        run(args.npm, "ci", "--include=optional", cwd=ROOT / "web")
        run(args.npm, "run", "build", cwd=ROOT / "web")
        shutil.copytree(ROOT / "web/dist", bundle / "web")
        parser = bundle / "pdf_parser"
        parser.mkdir()
        for directory in ("papers_parser", "paper_crf"):
            shutil.copytree(ROOT / "pdf_parser" / directory, parser / directory,
                            ignore=shutil.ignore_patterns("__pycache__", "test_*", "*.pyc", "weights"))
        for path in (ROOT / "pdf_parser").glob("*.py"):
            shutil.copy2(path, parser / path.name)
        if args.profile == "hybrid":
            models = json.loads((ROOT / "packaging/desktop/dependencies.json").read_text())["models"]
            weights = parser / "paper_crf/weights"
            weights.mkdir()
            for item in models:
                if item["file"] == "reference-text.crfsuite" and not args.include_reference_model:
                    continue
                source = args.model_dir / item["file"]
                if sha256(source) != item["sha256"]:
                    raise RuntimeError(f"production model differs from pinned model: {item['file']}")
                shutil.copy2(source, weights / source.name)
        extract(archives["python"], bundle)
        python = bundle / "python" / ("python.exe" if os.name == "nt" else "bin/python3")
        wheels = work / "wheels"
        requirements = ROOT / "packaging/desktop/requirements-runtime.lock"
        run(python, "-I", "-m", "pip", "download", "--only-binary=:all:", "--require-hashes",
            "-r", requirements, "--dest", wheels)
        run(python, "-I", "-m", "pip", "install", "--no-index", "--no-compile", "--only-binary=:all:",
            "--require-hashes", "--find-links", wheels, "-r", requirements)
        run(python, "-I", "-m", "pip", "check")
        packages = python_inventory(python, bundle / "python-packages.json")
        # Validate mandatory inference artifacts with fail-closed CRF mode.
        # Hybrid's normal fallback must not hide a broken release bundle.
        if args.profile == "hybrid":
            code = (
                "import sys,pathlib;sys.path.insert(0,sys.argv[1]);"
                "from papers_parser.engine_factory import build_extraction_engine;"
                "from papers_parser.crf_engine import HybridPaperExtractionEngine;"
                "e=build_extraction_engine('crf',pathlib.Path(sys.argv[1])/'paper_crf');"
                "assert isinstance(e,HybridPaperExtractionEngine);print('Required CRF models loaded')"
            )
            run(python, "-I", "-B", "-c", code, parser, cwd=work,
                env={**os.environ, "LOG_PATH": str(work / "model-validation.log")})
        wheel_record = [{"file": p.name, "sha256": sha256(p)} for p in sorted(wheels.glob("*.whl"))]
        write_json(bundle / "python-wheels.json", wheel_record)
        shutil.copy2(requirements, bundle / requirements.name)
        notices = bundle / "licenses"
        notices.mkdir()
        copy_notices(bundle / "python", notices / "python")
        if os.name == "nt":
            extract(archives["postgres_windows"], work / "pg")
            pg = work / "pg/pgsql"
            # The EDB ZIP also contains pgAdmin/Electron, another Python and
            # StackBuilder. Ship only database runtime directories.
            for directory in ("bin", "lib", "share"):
                shutil.copytree(pg / directory, bundle / "postgres" / directory)
            for name in ("server_license.txt", "commandlinetools_3rd_party_licenses.txt"):
                shutil.copy2(pg / name, notices / ("PostgreSQL-" + name))
            extract(archives["redis_windows"], work / "redis")
            redis_binary = next((work / "redis").rglob("redis-server.exe"))
            (bundle / "redis").mkdir()
            # Run the CLI directly; never ship the unused .NET service installer.
            for source in [redis_binary, *redis_binary.parent.glob("*.dll")]:
                shutil.copy2(source, bundle / "redis" / source.name)
        else:
            extract(archives["postgres_source"], work)
            pg = work / "postgresql-17.11"
            run(pg / "configure", f"--prefix={bundle / 'postgres'}", "--without-readline", "--without-zlib",
                "--without-icu", "--without-lz4", "--without-zstd", "--disable-rpath", cwd=pg)
            run("make", "-j2", cwd=pg)
            run("make", "install", cwd=pg)
            # Migration 28 requires this contrib extension; core PG install
            # alone starts successfully but cannot initialize the application.
            run("make", "-j2", cwd=pg / "contrib/pg_trgm")
            run("make", "install", cwd=pg / "contrib/pg_trgm")
            shutil.copy2(pg / "COPYRIGHT", notices / "PostgreSQL-COPYRIGHT")
            if platform.system() == "Darwin":
                relocate_macos_libraries(bundle / "postgres")
            extract(archives["redis_source"], work)
            redis = work / "redis-7.4.11"
            run("make", "-j2", "MALLOC=libc", "BUILD_TLS=no", cwd=redis)
            (bundle / "redis").mkdir()
            shutil.copy2(redis / "src/redis-server", bundle / "redis/redis-server")
            copy_notices(redis, notices / "redis")
        copy_notices(bundle / "postgres", notices / "postgres")
        copy_notices(ROOT / "web/node_modules", notices / "web")
        # Lock Go module identities and collect their shipped upstream license files.
        module_data = subprocess.check_output([str(args.go), "list", "-m", "-json", "all"], cwd=ROOT, text=True)
        decoder = json.JSONDecoder()
        modules = []
        while module_data.strip():
            module_data = module_data.lstrip()
            module, end = decoder.raw_decode(module_data)
            module_data = module_data[end:]
            modules.append({k: module[k] for k in ("Path", "Version", "Sum") if k in module})
            if "Dir" in module and not module.get("Main"):
                source = Path(module["Dir"])
                for path in source.iterdir():
                    if path.is_file() and path.name.upper().startswith(("LICENSE", "NOTICE", "COPYING")):
                        destination = notices / "go" / module["Path"] / path.name
                        destination.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copy2(path, destination)
        write_json(bundle / "go-modules.json", modules)
        components = [{"type": "library", "name": p["name"], "version": p["version"],
                       "purl": f"pkg:pypi/{p['name'].lower()}@{p['version']}"} for p in packages]
        components += [{"type": "library", "name": p["Path"], "version": p.get("Version", "local")}
                       for p in modules]
        components += [{"type": "application", "name": name, "version": version}
                       for name, version in (("PostgreSQL", "17.11"), ("Redis", "7.4.11"),
                                             ("CPython", "3.12.15"))]
        node_lock = json.loads((ROOT / "web/package-lock.json").read_text())
        components += [{"type": "library", "name": name.removeprefix("node_modules/"),
                        "version": p["version"]} for name, p in node_lock["packages"].items()
                       if name and "version" in p]
        write_json(bundle / "sbom.cdx.json", {"bomFormat": "CycloneDX", "specVersion": "1.6",
                                            "version": 1, "components": components})
        shutil.copy2(ROOT / "packaging/desktop/THIRD_PARTY_NOTICES.md", bundle)
        shutil.copy2(ROOT / "packaging/desktop/sources.lock.json", bundle)
        shutil.copy2(ROOT / "packaging/desktop/dependencies.json", bundle / "dependency-audit.json")
        shutil.copy2(ROOT / "web/package-lock.json", bundle / "web-packages.lock.json")
        (bundle / ".launcher_env").write_text(f"LAUNCHER_EXTRACTION_ENGINE={args.profile}\n", encoding="utf-8")
        shutil.copy2(ROOT / "packaging/desktop/BUNDLE.md", bundle / "README.md")
        # Generated caches/bytecode contain build paths and are not runtime inputs.
        for directory in list(bundle.rglob("__pycache__")):
            shutil.rmtree(directory)
        manifest = {"schema_version": 1, "distribution": "internal-validation-only",
                    "target": target, "profile": args.profile, "revision": args.revision,
                    "sources": lock, "public_release_approved": False, "files": inventory(bundle)}
        write_json(bundle / "bundle.json", manifest)
        lines = [f"{item['sha256']}  {name}" for name, item in manifest["files"].items() if "sha256" in item]
        lines.append(f"{sha256(bundle / 'bundle.json')}  bundle.json")
        (bundle / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")
        verify(bundle)
        bundle.rename(output)
    print(f"Internal bundle: {output}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--profile", choices=("rules", "hybrid"), default="hybrid")
    parser.add_argument("--model-dir", type=Path, default=ROOT / "pdf_parser/paper_crf/weights")
    parser.add_argument("--include-reference-model", action="store_true",
                        help="also ship the large, disabled-by-default reference-region model")
    parser.add_argument("--go", default="go")
    parser.add_argument("--npm", default="npm.cmd" if os.name == "nt" else "npm")
    args = parser.parse_args()
    args.cache = args.cache.resolve()
    assemble(args)


if __name__ == "__main__":
    main()
