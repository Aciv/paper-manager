#!/usr/bin/env python3
"""Exercise real setup filesystem failures using only disposable user data."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

from desktop_bundle import verify


def check(bundle: Path, output: Path):
    verify(bundle)
    launcher = bundle / ("launcher.exe" if os.name == "nt" else "launcher")
    steps = []
    with tempfile.TemporaryDirectory(prefix="pm setup failures 阅读 ") as temporary:
        root = Path(temporary) / "data"
        command = [str(launcher), "--data-dir", str(root), "--install-dir", str(bundle),
                   "--env-file", str(bundle / ".launcher_env"), "--open-browser=false"]

        def invoke(operation, **options):
            return subprocess.run(command + [operation], capture_output=True, timeout=90, **options)

        if invoke("--configure").returncode:
            raise RuntimeError("could not prepare the disposable setup configuration")
        # An existing object is not deleted to force initialization to succeed.
        obstruction = root / "cache"
        obstruction.write_text("preserve this object", encoding="utf-8")
        if invoke("--initialize").returncode == 0 or (root / "initialized.json").exists():
            raise RuntimeError("unwritable setup path was reported as completed")
        if obstruction.read_text(encoding="utf-8") != "preserve this object":
            raise RuntimeError("setup deleted an existing obstructing object")
        obstruction.unlink()  # only this probe's own fixture
        steps.append("existing obstruction preserved; failure did not publish initialization")
        if os.name != "nt":
            logs = root / "logs"
            logs.chmod(0o500)
            try:
                if invoke("--initialize").returncode == 0 or (root / "initialized.json").exists():
                    raise RuntimeError("read-only initialization logs were accepted")
            finally:
                logs.chmod(0o700)
            steps.append("ordinary-user write permission failure propagated without completion")
            # A per-process file-size quota makes real initdb writes fail. The
            # limit applies only to the disposable child, never the CI host.
            import resource

            def quota():
                resource.setrlimit(resource.RLIMIT_FSIZE, (16 << 10, 16 << 10))

            if invoke("--initialize", preexec_fn=quota).returncode == 0 or (root / "initialized.json").exists():
                raise RuntimeError("initdb storage quota failure was accepted")
            if (root / "postgres").exists():
                raise RuntimeError("failed initdb published a partial cluster")
            steps.append("real initdb file quota failure preserved retryability")
        # Explicit retry succeeds using exactly the original credentials.
        original = (root / "runtime.json").read_bytes()
        if invoke("--initialize").returncode or not (root / "initialized.json").is_file():
            raise RuntimeError("explicit setup retry failed")
        if (root / "runtime.json").read_bytes() != original:
            raise RuntimeError("setup retry replaced runtime configuration")
        if invoke("--initialize").returncode:
            raise RuntimeError("idempotent setup validation failed")
        steps.append("explicit retry and repeated setup succeeded without replacement credentials")
    output.write_text(json.dumps({"status": "passed", "steps": steps}, indent=2) + "\n", encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    check(args.bundle.resolve(), args.output.resolve())


if __name__ == "__main__":
    main()
