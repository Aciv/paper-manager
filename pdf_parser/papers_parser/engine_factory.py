"""Extraction-engine construction kept out of the lightweight server process."""
from __future__ import annotations

from pathlib import Path

from logger import get_logger
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine

LOG = get_logger(__name__)


def build_extraction_engine(mode: str, model_dir: Path):
    """Construct rules/CRF/hybrid/shadow once; only CRF mode is fail-closed."""
    rules = DeterministicPaperExtractionEngine()
    if mode == "rules":
        return rules
    # Keep rules-only extraction free of CRF/model_train imports. Hybrid,
    # shadow, and CRF modes intentionally load the learned stack.
    from papers_parser.crf_engine import (
        CRFPaperExtractionEngine,
        HybridPaperExtractionEngine,
        ShadowPaperExtractionEngine,
    )
    try:
        crf = CRFPaperExtractionEngine(
            header_model=model_dir / "phase21" / "header-text-title-priors.crfsuite",
            reference_model=model_dir / "phase21" / "reference-text.crfsuite",
            citation_model=model_dir / "phase22" / "citation-context.crfsuite",
        )
    except Exception:
        if mode == "crf":
            raise
        LOG.warning("%s CRF artifacts unavailable; using deterministic rules", mode, exc_info=True)
        return rules
    summary = crf.model_summary()
    LOG.info(
        "paper CRF artifacts loaded header=%s header_features=%s "
        "reference=%s reference_features=%s citation=%s citation_features=%s",
        summary["header_model"],
        summary["header_feature_version"],
        summary["reference_model"],
        summary["reference_feature_version"],
        summary["citation_model"],
        summary["citation_feature_version"],
    )
    if mode == "crf":
        return crf
    if mode == "shadow":
        return ShadowPaperExtractionEngine(rules, crf)
    return HybridPaperExtractionEngine(rules, crf)
