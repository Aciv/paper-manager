# Python parser worker

This worker implements `DocumentParser.Recognize` over gRPC and currently
accepts PDFs only. It invokes `pymupdf4llm.to_markdown(..., page_chunks=True)`
and returns Markdown bytes plus selectable page-layout blocks to the Go API.
The API owns document status, Markdown storage, Block creation, and all
PostgreSQL writes.

Generate stubs and run it after starting the API stack:

```bash
../.venv/bin/pip install -r requirements.txt
../.venv/bin/python -m grpc_tools.protoc -I../proto --python_out=. --grpc_python_out=. ../proto/document_parser.proto
PARSER_GRPC_ADDR=127.0.0.1:50051 ../.venv/bin/python -m papers_parser.server
```

The Go API and worker need the same `UPLOAD_DIR` only when Go supplies a
shared file path. Without an explicit `UPLOAD_DIR`, the worker uses the repository's
`data/uploads` directory, regardless of its current working directory.
