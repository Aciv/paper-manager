# paper_crf

Runtime-owned paper CRF stack. This directory is the release boundary:
`pdf_parser/papers_parser` loads CRF code and artifacts from here and never
imports `model_train/`.

## Layout (three forms)

```text
paper_crf/
  models/    # 模型: feature extraction, schema, decoding (pure inference code)
  weights/   # 权重: trained .crfsuite parameter files
  specs/     # 规范: artifact manifests + bundle.json (selection, thresholds)
```

`specs/bundle.json` selects one weight/spec pair for each runtime role
(`header`, `reference`, `citation`) and owns the calibrated thresholds. Paths
are relative to this directory. `papers_parser.engine_factory` loads this
bundle; an explicit `PAPER_CRF_MODEL_DIR` points at another directory with the
same layout (for evaluating an unreleased bundle).

The **reference-region CRF is opt-in and disabled by default**
(`PAPER_REFERENCE_CRF_ENABLED=false`). When disabled, the reference model is
not opened (startup memory and per-document inference are skipped) and
reference entries stay rule-owned; header and citation CRFs are unaffected.
Set the flag to true only to evaluate the reference region. With
`load_bundle(..., require_reference=False)`, a slim bundle may omit the
reference role entirely.

## Runtime validation

`papers_parser.crf_engine._CRF` validates, before opening a model:

- the manifest `schema_version` is `paper-crf-artifact/v1`;
- the manifest `feature_version` matches the code (`paper-line-crf/v2`, with
  `paper-line-crf/v1` still accepted for the retained reference model;
  `paper-citation-crf/v1` for the citation model).

A missing or invalid required bundle makes `hybrid`/`shadow` fall back to
deterministic rules with a warning; explicit `crf` mode fails closed for the
header/citation artifacts. The reference artifact is validated only when
`PAPER_REFERENCE_CRF_ENABLED=true`; with the flag enabled, `crf` mode returns
the full header+reference+citation cascade. CRF is optional, so the parser image does
not require the weights; a release that intends to ship CRF should verify the
bundle in its own release test.

## Promoting a trained model

`model_train/` remains the training workspace; its `artifacts/` tree is not the
runtime source. To promote a trained artifact:

1. Train/evaluate in `model_train/` and choose the artifact triple.
2. Copy each selected `.crfsuite` into `weights/` and its `.json` manifest into
   `specs/`. Keep the manifest metadata accurate for provenance.
3. Update `specs/bundle.json` paths, and update the thresholds only from the
   same evaluation run that selected the artifacts.
4. Run the parser test suite, then test an explicit `crf` load. If changing the
   reference artifact, test with `PAPER_REFERENCE_CRF_ENABLED=true`. Record the
   chosen feature versions and provenance in the bundle if they change.

## Version control

The `.crfsuite` files are release artifacts, not source. The reference model is
about 115 MB, which exceeds GitHub's 100 MiB per-file limit, so
`weights/*.crfsuite` is git-ignored. A release build or package must supply the
weights from the training workspace (or a future artifact store); `specs/` and
`models/` are tracked. `model_train/` keeps its own training copy and is
intentionally unchanged; when promoting an artifact, verify that the runtime
`models/` feature code and feature versions still match the training run that
produced it.
