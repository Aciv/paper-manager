# API Smoke Scripts

## Single-PDF parser and extraction benchmark

Run recognition plus the selected paper-extraction engine without starting the
API, Redis, PostgreSQL, or a gRPC server. It prints title/authors/reference
candidates and separate recognition/extraction timings:

```bash
.venv/bin/python scripts/benchmark_paper_extraction_pdf.py path/to/paper.pdf \
  --engine hybrid --output /tmp/paper-extraction.json
```

Use `--engine rules` to compare the fallback, `--engine shadow` to return the
rules result while logging aggregate CRF disagreements, or `--engine crf` to
fail fast if the selected artifacts are missing/incompatible. Artifacts default to
`model_train/artifacts`; override with `--model-dir` when testing a promoted
model directory.

Start the API stack first, then run focused checks from the repository root:

```bash
./scripts/test_health_api.sh
./scripts/test_upload_api.sh
./scripts/test_document_crud_api.sh
./scripts/test_paper_metadata_api.sh
./scripts/test_paper_info_api.sh
./scripts/test_remote_import_api.sh
./scripts/test_reference_acquisition_api.sh
./scripts/test_document_base_api.sh
./scripts/test_cancel_api.sh
./scripts/test_processing_api.sh
```

Run everything with:

```bash
./scripts/test_all_api.sh
# The former entry point remains supported.
./scripts/test_document_api.sh
```

All scripts accept `API_BASE_URL` (default `http://localhost:8080`) and create
isolated temporary fixtures, DOI values, documents, and document bases. They
delete their created resources on exit. `test_processing_api.sh` requires a
real PDF (`PDF_FILE`, default `DOne_Decoupling_Structure_Rendering.pdf`) and a
working builtin parser. Set `SKIP_PROCESS_TEST=1` when only API CRUD coverage
is needed. Set `REQUIRE_IMAGE_BLOCKS=1` to require the processing fixture to
produce at least one rendered image/table block.

`test_paper_info_api.sh` verifies forward/reverse citation endpoints, the
persistent processing response, and extraction/source/reference retry
controls. `test_reference_acquisition_api.sh` verifies acquisition
start/cancel, processing-span visibility, compact progress shape, and source
status isolation. Bounded fan-out is checked by
`TestDispatchReferenceDownloadsHonorsConfiguredLimit`.

Default scripts do not require public provider availability.
`test_remote_import_api.sh` always checks malformed URLs, SSRF rejection, and
unsupported generic landing pages. Set `RUN_LIVE_INTEGRATION_TESTS=1` to also
download two public arXiv PDFs through an explicit PDF URL and a DOI URL:

```bash
RUN_LIVE_INTEGRATION_TESTS=1 ./scripts/test_remote_import_api.sh
PAPER_CATALOG_LIVE_TEST=1 go test ./internal/infrastructure/papercatalog \
  -run '^TestLivePaperCatalogProviders$' -v
```

Public URLs can be overridden with `REMOTE_PDF_DIRECT_URL` and
`REMOTE_PDF_DOI_URL`. Live remote imports require the complete parser stack;
set `REMOTE_IMPORT_REQUIRE_READY=0` only when testing download persistence
without requiring successful recognition. Catalog no-match, provider outage,
and retry exhaustion are operational Asynq outcomes and are covered
deterministically by focused Go tests. Stale-input guards are covered by
`TestPaperInfoExtractMetadataRejectsStatusChangedDuringRPC`; catalog result
generation/state guards are covered by the dispatcher result tests.
Cancellation and deletion remain covered by `test_cancel_api.sh` and the
document CRUD script. Candidate ambiguity has no user-selection API by design: the resolver
uses deterministic scoring and only persists a safe automatic match.
