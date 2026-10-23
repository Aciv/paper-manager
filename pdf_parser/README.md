# Python parser server and workers

The process exposes three independent gRPC services:

- `DocumentParser.Recognize` accepts PDFs, invokes
  `pymupdf4llm.to_markdown(..., page_chunks=True)`, and returns Markdown plus
  selectable page-layout blocks and one bounded source outline. Embedded PDF
  bookmarks win when at least one destination is usable; otherwise headings
  are derived from that call's final cleaned Markdown. Outline schema version
  1 uses parser block ordinals and Unicode code-point ranges.
- `PaperMetadataExtractor.Extract` accepts one bounded Markdown revision and a
  compact source map, then returns deterministic title, author, abstract, raw
  reference, confidence, and evidence candidates. It performs no catalog
  lookup and writes no database state.
- `PdfRenderer.RenderPage` synchronously renders a full PDF page or normalized
  page bbox to a bounded PNG. It is not an Asynq task and uses only PyMuPDF.

The Go API owns document status, Markdown storage, Block creation, catalog
resolution, canonical metadata, and all PostgreSQL writes.

## Server / worker topology

`papers_parser.server` is intentionally lightweight. It imports only gRPC,
protobuf, and the worker-pool runtime; it does **not** import PyMuPDF,
PyMuPDF4LLM, ONNX Runtime, CRF, or NumPy. Heavy work runs in kind-specific
worker processes:

- `papers_parser.worker --kind recognize` loads PyMuPDF4LLM and the layout
  stack, and handles `DocumentParser.Recognize`;
- `papers_parser.worker --kind extract` loads PyMuPDF plus the CRF/rules
  engine, and handles `PaperMetadataExtractor.Extract`. It does not import
  PyMuPDF4LLM/ONNX;
- `papers_parser.worker --kind render` imports only PyMuPDF and serves
  `PdfRenderer.RenderPage` for full-page or normalized-region PNGs. It never
  loads PyMuPDF4LLM/ONNX/CRF and can stay warm longer for frequent page reads.

Each kind has its own bounded pool. Workers start on demand, are reused while
busy, and exit after `PARSER_WORKER_IDLE_SECONDS` (or after
`PARSER_WORKER_MAX_JOBS`). The server remains running with a small resident
set; the external gRPC contract is unchanged. Configure with:

```env
PARSER_RECOGNIZE_WORKER_MAX=4
PARSER_EXTRACT_WORKER_MAX=2
PARSER_RENDER_WORKER_MAX=4
PARSER_WORKER_IDLE_SECONDS=300
PARSER_RENDER_WORKER_IDLE_SECONDS=600
PARSER_WORKER_MAX_JOBS=50
PARSER_WORKER_QUEUE_TIMEOUT=30
PARSER_WORKER_START_TIMEOUT=90
PARSER_RENDER_TIMEOUT=15
PARSER_RENDER_MAX_DPI=300
PARSER_RENDER_MAX_PIXELS=20000000
PARSER_RENDER_MAX_OUTPUT_BYTES=12582912
```

`PARSER_MAX_WORKERS` and `PARSER_MAX_CONCURRENT_RPCS` still bound the public
gRPC server. Worker maxima are independent and should be chosen from the
available memory budget: an idle parser costs tens of MiB, while each active
worker carries the relevant PyMuPDF/ONNX/CRF working set.

Worker stdout/stderr is captured by the server and logged with a
`worker[recognize]` / `worker[extract]` prefix. Worker sockets live in a
private temporary directory and are removed on exit.

## Server / worker interaction

```text
Go API
  -> external gRPC (DocumentParser / PaperMetadataExtractor)
     -> server proxy serializes the protobuf request
        -> WorkerPool(kind).call(method, payload)
           -> reuse idle worker, or start one
              -> Unix socket frame request
                 -> worker parses protobuf and runs the service
              <- Unix socket frame response
           <- proxy parses protobuf response
     <- external gRPC response
```

### Starting a worker

`WorkerPool._acquire()` first reuses an idle worker. If none is available and
the kind's worker count is below its maximum, it reserves a start slot and
`_spawn()` runs:

```text
python -m papers_parser.worker
  --kind recognize|extract
  --socket <private-temp-dir>/worker.sock
  --config <base64 JSON: upload dir, limits, engine, model dir>
  --idle-seconds ...
  --max-jobs ...
```

The worker selects its heavy imports only after parsing `--kind`, builds its
service, binds the Unix socket, then waits for the server connection. The
server retries the connection until `PARSER_WORKER_START_TIMEOUT`; connect
success is the readiness signal (heavy imports/model loading happen before the
bind). Starts can proceed concurrently up to the per-kind maximum. If all
workers are busy, the request waits up to `PARSER_WORKER_QUEUE_TIMEOUT` and
then returns `RESOURCE_EXHAUSTED`.

### Request / response frames

The private protocol is a length-prefixed frame over the Unix socket:

```text
request:  method:uint8 | length:uint32 | protobuf payload
response: status:uint8 | length:uint32 | protobuf payload or JSON error
```

Methods are `1 = Recognize`, `2 = Extract`, `3 = RenderPage`. A successful
response is the serialized `RecognitionResult` / `PaperExtractionResult` /
`PdfRenderResult`; a failure is
`{"code": "INVALID_ARGUMENT|UNAVAILABLE|...", "message": "..."}`. Worker
errors are mapped back to gRPC status codes by the server proxy.

`PdfRenderer.RenderPage` is synchronous and is not an Asynq task. The Go
application passes an internal relative `source_storage_key`; the render
worker resolves it below the shared `UPLOAD_DIR`, renders a full page or a
normalized `bbox` with PyMuPDF, and returns a bounded PNG plus pixel/page
dimensions. Page rendering has its own pool, timeout, and DPI/pixel/output
limits. A render worker can stay warm longer than recognition/extraction
workers, so repeated page reads reuse the process; an application-level cache
can be added later without changing this RPC.

### Cancellation, idle exit, and shutdown

- The proxy checks the external gRPC context while waiting; a cancelled or
  expired request sends `SIGUSR1` to the worker and closes that worker.
- The worker's own `PARSER_RPC_MAX_SECONDS` checkpoints remain active.
- A worker exits after `PARSER_WORKER_IDLE_SECONDS`, after
  `PARSER_WORKER_MAX_JOBS`, on socket EOF, or on SIGTERM.
- The server monitor closes idle workers and keeps the lightweight process
  alive; a crashed worker is retired and the next request starts a new one.
- Server shutdown stops accepting RPCs, drains in-flight requests within
  `PARSER_SHUTDOWN_GRACE_SECONDS`, then stops the pools and their process
  groups.

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
blocks. `DocumentParser.Recognize` emits Markdown, semantic Blocks, and the
selected outline in one generation. Outline failures are non-fatal and return
an explicit `none` source with bounded diagnostics.
For `hybrid` and `crf`, the extraction RPC receives the Go-created relative PDF
storage key and lazily rebuilds bounded, temporary PDF-line facts from the
original PDF inside the configured shared upload directory. The Python parser
owns that line-layout schema and validation; Go neither stores nor exposes it.
Rules do not reopen the PDF, and a missing/stale source degrades to rules.

`PaperExtractionEngine` in `papers_parser/paper_extractor.py` is the
replacement boundary. The `extract` worker constructs the engine and injects
it plus request limits into `PaperMetadataExtractionRPC`. Choose its runtime
mode with `PAPER_EXTRACTION_ENGINE=rules|hybrid|shadow|crf`.

CRF runtime code, weights, and specs live in `pdf_parser/paper_crf/`:

```text
paper_crf/
  models/    # feature extraction, schema, decoding
  weights/   # selected .crfsuite files
  specs/     # artifact manifests + bundle.json
```

`specs/bundle.json` selects the header/reference/citation artifacts and owns
the calibrated thresholds. The default bundle root is this package;
`PAPER_CRF_MODEL_DIR` overrides it for evaluating another promoted bundle.
The reference-region CRF is opt-in (`PAPER_REFERENCE_CRF_ENABLED=false` by
default): when disabled its model is not opened and reference entries remain
rule-owned; header and citation CRFs are unaffected. `hybrid` uses CRF facts
only above calibrated confidence and falls back to rules when models or layout
facts are unavailable. `shadow` returns only rule output and logs aggregate,
content-free header disagreement counters (reference comparison is skipped
when the reference model is disabled). Explicit `crf` is the full
header+reference+citation cascade when the reference flag is enabled; otherwise
it composes the header/citation CRFs with rule-owned references. It fails when
the first extract worker starts if the required artifacts are missing or
incompatible; the failure is returned to the Go caller as an unavailable
parser request rather than preventing the lightweight server from listening. Compose bakes `pdf_parser/paper_crf` into the parser
image and uses `hybrid` by default. `model_train/` is the training workspace
and is not imported at runtime. Worker startup logs artifact names and feature
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
parser and workers write structured runtime messages to standard output; the
server prefixes captured worker output and `docker compose logs -f parser`
shows both. A log-path failure never interrupts an RPC. Local non-container
runs retain the default `pdf_parser/Logs/today.log`.

## PyMuPDF page renderer

`papers_parser/render_pdf_region.py` is the PyMuPDF-only renderer used by the
`render` worker. It supports a full page or one normalized page bbox and
returns PNG bytes plus page/pixel dimensions. It does not import
`pymupdf4llm`, ONNX, or CRF.

The application service owns document authorization, the internal storage key,
the render deadline/concurrency limits, PNG validation, and optional caching.
The parser server owns the render worker pool; the Go API never runs Python.
The same renderer is still usable as a standalone CLI for diagnostics.

For an isolated local check from `pdf_parser/`:

```bash
../.venv/bin/python -m papers_parser.render_pdf_region \
  --source ../data/uploads/pdf/example.pdf \
  --page-number 1 \
  --bbox-x0 72 --bbox-y0 72 --bbox-x1 300 --bbox-y1 240 \
  --dpi 216 --max-dpi 300 --max-pixels 8000000 > region.png
```
