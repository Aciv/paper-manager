# Python parser worker

This worker exposes two independent gRPC services:

- `DocumentParser.Recognize` accepts PDFs, invokes
  `pymupdf4llm.to_markdown(..., page_chunks=True)`, and returns Markdown plus
  selectable page-layout blocks.
- `PaperMetadataExtractor.Extract` accepts one bounded Markdown revision and a
  compact source map, then returns deterministic title, author, abstract, raw
  reference, confidence, and evidence candidates. It performs no catalog
  lookup and writes no database state.

The Go API owns document status, Markdown storage, Block creation, catalog
resolution, canonical metadata, and all PostgreSQL writes.

Generate stubs and run it after starting the API stack:

```bash
../.venv/bin/pip install -r requirements.txt
../.venv/bin/python -m grpc_tools.protoc -I../proto --python_out=. --grpc_python_out=. ../proto/document_parser.proto
PARSER_GRPC_ADDR=127.0.0.1:50051 ../.venv/bin/python -m papers_parser.server
```

Generate the Go stubs from the repository root:

```bash
PATH=/root/go/bin:$PATH .venv/bin/python -m grpc_tools.protoc \
  -Iproto \
  --go_out=gen/papers/v1 --go_opt=paths=source_relative \
  --go-grpc_out=gen/papers/v1 --go-grpc_opt=paths=source_relative \
  proto/document_parser.proto
```

Paper extraction accepts at most 32 MiB of valid UTF-8 Markdown, 50,000 source
blocks. `DocumentParser.Recognize` emits Markdown plus semantic Blocks only.
For `hybrid` and `crf`, the extraction RPC receives the Go-created relative PDF
storage key and lazily rebuilds bounded, temporary PDF-line facts from the
original PDF inside the configured shared upload directory. The Python parser
owns that line-layout schema and validation; Go neither stores nor exposes it.
Rules do not reopen the PDF, and a missing/stale source degrades to rules.

`PaperExtractionEngine` in `papers_parser/paper_extractor.py` is the
replacement boundary. `server.py` constructs both gRPC servicers and injects
the engine plus request limits into `PaperMetadataExtractionRPC`. Choose its
runtime mode with `PAPER_EXTRACTION_ENGINE=rules|hybrid|shadow|crf` and point it at a
readable artifact directory with `PAPER_CRF_MODEL_DIR`. `hybrid` uses CRF facts
only above calibrated confidence and falls back to rules when models or layout
facts are unavailable. `shadow` returns only rule output and logs aggregate,
content-free CRF disagreement counters. Explicit `crf` fails at startup for missing or
incompatible artifacts. Compose mounts `model_train/artifacts` read-only at
`/models` and uses `hybrid` by default. Startup logs artifact names and feature
versions, never extracted text.

Override request limits with
`PARSER_MAX_EXTRACTION_MARKDOWN_BYTES` and
`PARSER_MAX_EXTRACTION_BLOCKS` / `PARSER_MAX_EXTRACTION_LAYOUT_LINES` (or the
matching command-line flags). The layout-line limit applies only to Python's
temporary lazy extraction, not to a gRPC payload or database artifact.

`PaperExtractionResult.references` remains the compatibility list of raw
reference text. Its additive `reference_candidates` field can carry partial
author/title/venue/year/DOI/arXiv/volume/page fields for the same raw entry.
The deterministic engine sends raw-only candidates; the optional runtime
Citation CRF populates fields when a reference entry is confidently isolated.
The Go API validates each candidate
and applies only non-empty fields to the corresponding unknown-paper metadata.

The Go API and worker need the same `UPLOAD_DIR` only when Go supplies a
shared file path. Without an explicit `UPLOAD_DIR`, the worker uses the repository's
`data/uploads` directory, regardless of its current working directory.

Parser logs use `LOG_PATH` when its parent directory is writable. In the
read-only Compose container that path is intentionally unavailable, so the
parser automatically writes structured runtime messages to standard output;
use `docker compose logs -f parser`. A log-path failure never interrupts an
RPC. Local non-container runs retain the default `python/Logs/today.log`.

## One-shot PDF region renderer foundation

`papers_parser/render_pdf_region.py` is a framework-level CLI for a future Go
caller. It imports only PyMuPDF and renders one page bbox to PNG on stdout; it
does not start gRPC or import `pymupdf4llm`, ONNX, or the CRF runtime. The Go
adapter owns storage-key containment, process concurrency/cancellation, output
limits, PNG validation, and temporary-file cleanup.

There is intentionally no application-service call, HTTP route, frontend
action, dependency-injection binding, environment configuration, or Docker
packaging yet. The eventual caller will also own authorization and persistence
of any derived image through `FileStorage`.

For an isolated local check from `python/`:

```bash
../.venv/bin/python -m papers_parser.render_pdf_region \
  --source ../data/uploads/pdf/example.pdf \
  --page-number 1 \
  --bbox-x0 72 --bbox-y0 72 --bbox-x1 300 --bbox-y1 240 \
  --dpi 216 --max-dpi 300 --max-pixels 8000000 > region.png
```
