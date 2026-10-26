# Scripts

Scripts are grouped by purpose. Tests have one unified entry point per type and
one dispatcher at the repository root.

## Test entry points

```bash
./scripts/run_tests.sh go        # Go backend tests: go test ./...
./scripts/run_tests.sh python    # Python parser tests: pdf_parser/papers_parser/test
./scripts/run_tests.sh web       # Frontend node --test suites
./scripts/run_tests.sh desktop   # Desktop packaging/runtime tests
./scripts/run_tests.sh api       # API smoke tests (requires a running stack)
./scripts/run_tests.sh all       # go + python + web + desktop
RUN_API_TESTS=1 ./scripts/run_tests.sh all   # include API smoke tests
```

`all` deliberately excludes the API smoke suite because it needs a live API,
PostgreSQL, Redis, and the parser. Run it explicitly with `api`.

The category scripts are thin wrappers:

| Category | Runner | Contents |
| --- | --- | --- |
| Go backend | `scripts/tests/go.sh` | `go test ./...` (extra arguments are forwarded) |
| Python parser | `scripts/tests/python.sh` | `unittest` discovery in `pdf_parser/papers_parser/test` |
| Web | `scripts/tests/web.sh` | `test:selection`, `test:doi`, `test:bibliography`, `test:translation`, `test:reading-note` |
| API smoke | `scripts/tests/api.sh` → `scripts/api-tests/run_all.sh` | Health, upload, CRUD, selection, metadata, citations, imports, bases, cancel, processing |
| Desktop | `scripts/tests/desktop.sh` → `scripts/desktop/run_tests.sh` | Desktop Python packaging tests plus Go launcher/runtime tests |

The desktop runner also accepts `--python-only` and `--go-only`; CI uses the
Python-only mode where Go is not set up.

## Directory layout

```text
scripts/
  run_tests.sh          # dispatcher for every suite
  tests/                # one runner per test category
  api-tests/            # API smoke scripts, shared lib, and run_all.sh
  desktop/              # desktop build/package/validation tools
  desktop/tests/        # desktop Python tests
  parser/               # parser benchmarks and recognition checks
  dev/                  # dev.sh, task_map.sh, taskmap
```

## API smoke tests

Start the API stack first, then run the whole suite:

```bash
./scripts/api-tests/run_all.sh
# Backward-compatible alias:
./scripts/api-tests/test_document_api.sh
```

Focused checks live in the same folder:

```bash
./scripts/api-tests/test_health_api.sh
./scripts/api-tests/test_upload_api.sh
./scripts/api-tests/test_document_crud_api.sh
./scripts/api-tests/test_paper_metadata_api.sh
./scripts/api-tests/test_paper_info_api.sh
./scripts/api-tests/test_remote_import_api.sh
./scripts/api-tests/test_reference_acquisition_api.sh
./scripts/api-tests/test_document_base_api.sh
./scripts/api-tests/test_cancel_api.sh
./scripts/api-tests/test_processing_api.sh
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
RUN_LIVE_INTEGRATION_TESTS=1 ./scripts/api-tests/test_remote_import_api.sh
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
document CRUD script. Candidate ambiguity has no user-selection API by design:
the resolver uses deterministic scoring and only persists a safe automatic
match.

## Parser and desktop tooling

Single-PDF recognition and extraction benchmark (no API/DB/Redis):

```bash
.venv/bin/python scripts/parser/benchmark_paper_extraction_pdf.py path/to/paper.pdf \
  --engine hybrid --output /tmp/paper-extraction.json
```

Use `--engine rules` to compare the fallback, `--engine shadow` to return the
rules result while logging aggregate CRF disagreements, or `--engine crf` for
header/citation CRF with rule-owned references. Reference entries stay
rule-owned by default; add `--reference-crf-enabled` (the default follows
`PAPER_REFERENCE_CRF_ENABLED`) to evaluate the optional reference-region CRF.
With that flag, `--engine crf` restores the full header+reference+citation
cascade. The runtime bundle defaults to `pdf_parser/paper_crf`; override
`--model-dir` when testing another promoted bundle.

Other parser and desktop tools:

```bash
.venv/bin/python scripts/parser/benchmark_parser_shared.py --engine rules --jobs 8 --mixed-jobs 20 --idle-seconds 15 --output tmp/shared.json
.venv/bin/python scripts/parser/check_recognition_storage_key.py --output-dir tmp/recognition-key
.venv/bin/python scripts/desktop/build_desktop_bundle.py --profile hybrid
.venv/bin/python scripts/desktop/check_desktop_launcher.py --output-dir tmp/launcher
.venv/bin/python scripts/desktop/check_private_postgres.py --bin-dir "<PostgreSQL 17 bin>" --output tmp/postgres.json
```

Desktop build, validation, and packaging details are in
`packaging/desktop/`. Development helpers live in `scripts/dev/`:

```bash
./scripts/dev/dev.sh all       # Makefile `make dev` delegates here
./scripts/dev/task_map.sh      # Asynq topology; -check fails on an unhandled task
```
