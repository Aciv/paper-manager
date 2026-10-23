"""Extraction-engine construction kept out of the lightweight server process."""
from __future__ import annotations

from pathlib import Path

from logger import get_logger
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine

LOG = get_logger(__name__)


def build_extraction_engine(
    mode: str,
    model_dir: Path,
    reference_crf_enabled: bool = False,
):
    """Construct rules/CRF/hybrid/shadow once; only CRF mode is fail-closed.

    ``model_dir`` is the ``paper_crf`` bundle root (``models/``, ``weights/``,
    ``specs/``), not a training artifact directory. The reference-region CRF
    is optional and disabled by default; references stay rule-owned unless
    ``reference_crf_enabled`` is true. Header and citation CRFs are always
    loaded for non-rules modes. With the flag enabled, ``crf`` mode returns
    the original full header+reference+citation cascade; with it disabled,
    ``crf`` composes header/citation CRF with rule-owned references.
    """
    rules = DeterministicPaperExtractionEngine()
    if mode == "rules":
        return rules
    # Keep rules-only extraction free of CRF/paper_crf imports. Hybrid,
    # shadow, and CRF modes intentionally load the learned stack.
    from paper_crf import load_bundle
    from papers_parser.crf_engine import (
        CRFPaperExtractionEngine,
        HybridPaperExtractionEngine,
        ShadowPaperExtractionEngine,
    )
    try:
        bundle = load_bundle(model_dir, require_reference=reference_crf_enabled)
        reference = bundle.models["reference"] if reference_crf_enabled else None
        crf = CRFPaperExtractionEngine(
            header_model=bundle.models["header"].weights,
            reference_model=reference.weights if reference else None,
            citation_model=bundle.models["citation"].weights,
            header_use_layout=bundle.runtime.header_use_layout,
            reference_use_layout=bundle.runtime.reference_use_layout if reference else False,
            header_threshold=bundle.runtime.header_threshold,
            reference_threshold=bundle.runtime.reference_threshold,
            header_manifest=bundle.models["header"].spec,
            reference_manifest=reference.spec if reference else None,
            citation_manifest=bundle.models["citation"].spec,
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
        summary.get("reference_model", "disabled"),
        summary.get("reference_feature_version", "n/a"),
        summary["citation_model"],
        summary["citation_feature_version"],
    )
    if mode == "crf":
        # With the reference model enabled, keep the original full-CRF
        # semantics (header + reference + citation) for evaluation. When it is
        # disabled, compose header/citation CRF with rule-owned references.
        if reference_crf_enabled:
            return crf
        return HybridPaperExtractionEngine(rules, crf, prefer_crf_header=True, name="paper-crf")
    if mode == "shadow":
        return ShadowPaperExtractionEngine(rules, crf)
    return HybridPaperExtractionEngine(rules, crf)
