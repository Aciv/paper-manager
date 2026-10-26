"""Fail image construction if either shipped CRF model cannot run."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import sys


def main() -> None:
    parser = Path("/app/pdf_parser")
    sys.path.insert(0, str(parser))
    weights = parser / "paper_crf/weights"
    expected = {"header-text-title-priors.crfsuite", "citation-context.crfsuite"}
    if {path.name for path in weights.iterdir()} != expected:
        raise RuntimeError("parser image must contain exactly the two shipped small models")
    manifest = json.loads(Path("/app/model-manifest.json").read_text())
    models = {item["file"]: item for item in manifest["models"]}
    for name in sorted(expected):
        with (weights / name).open("rb") as stream:
            digest = hashlib.file_digest(stream, "sha256").hexdigest()
        if digest != models[name]["sha256"]:
            raise RuntimeError(f"shipped model hash differs: {name}")
    from papers_parser.crf_engine import HybridPaperExtractionEngine
    from papers_parser.engine_factory import build_extraction_engine

    engine = build_extraction_engine("crf", parser / "paper_crf")
    if not isinstance(engine, HybridPaperExtractionEngine):
        raise RuntimeError("required CRF models did not load")
    print("Both required CRF models loaded; optional reference model excluded")


if __name__ == "__main__":
    main()
