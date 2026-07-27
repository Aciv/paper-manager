# API Smoke Scripts

Start the API stack first, then run focused checks from the repository root:

```bash
./scripts/test_health_api.sh
./scripts/test_upload_api.sh
./scripts/test_document_crud_api.sh
./scripts/test_paper_metadata_api.sh
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
