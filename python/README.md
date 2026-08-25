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

Paper extraction accepts at most 32 MiB of valid UTF-8 Markdown and 50,000
source blocks. The `PaperExtractionEngine` protocol in
`papers_parser/paper_extractor.py` is the replacement boundary for a future
CRF implementation. `server.py` is the composition root: it constructs both
gRPC servicers and injects the extraction engine plus request limits into
`PaperMetadataExtractionRPC`. Override those limits with
`PARSER_MAX_EXTRACTION_MARKDOWN_BYTES` and
`PARSER_MAX_EXTRACTION_BLOCKS` (or the matching command-line flags).

The Go API and worker need the same `UPLOAD_DIR` only when Go supplies a
shared file path. Without an explicit `UPLOAD_DIR`, the worker uses the repository's
`data/uploads` directory, regardless of its current working directory.
