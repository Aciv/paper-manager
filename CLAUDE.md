# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, Test, and Run Commands

```bash
# Compile Go API
go build ./...

# Run local API (after setting up PostgreSQL and Redis)
cp .env.example .env
go run ./cmd/server

# Run all Go tests
go test ./...

# Run a single Go test
go test ./internal/application/service/ -run TestDocumentUpload -v

# Frontend
cd web && npm ci && npm run dev       # Vite dev server
cd web && npm run build               # Type-check and production build

# Full stack via Docker
docker compose up --build -d
docker compose down                   # stop, keep DB volume
docker compose down -v                # stop, delete DB volume
```

## Architecture Overview

This is a three-service papers manager: **Go HTTP API** (Gin), **React/TypeScript frontend** (Vite), and **Python gRPC parser** (pymupdf4llm). Async work flows through Redis-backed Asynq queues.

### Dependency Injection

`internal/container/container.go` wires the entire application via `uber/dig`. Every provider (config, database, repositories, services, handlers, router) is registered in `Build()`. Handlers receive their dependencies through a `dig.In` struct. Never instantiate dependencies manually — add them to the container.

### Request Flow (layered, top-down)

1. **`cmd/server/main.go`** — entry point. Loads `.env`, initializes logger, builds DI container, starts Asynq server, runs HTTP server with graceful shutdown.
2. **`internal/middleware/`** — chain: `RequestID` → `Logger` → `ErrorHandler` → `Recovery`. `ErrorHandler` owns the JSON error response; handlers must use `c.Error(serverErr)` + `c.Abort()`, never ad-hoc JSON.
3. **`internal/router/router.go`** — route registration. Split across files: `ServerFile` (upload/search/content/blocks), `ServerProcess` (cancel), `ServerPaperMetadata` (paper CRUD, citations, paper-info status/retry), `ServerDocumentBase` (collections).
4. **`internal/handler/`** — HTTP handlers. Parse requests, call services, return responses. Only handlers touch `gin.Context`.
5. **`internal/application/service/`** — business logic. Orchestrates repositories, file storage, and async task enqueueing.
6. **`internal/application/repository/`** — GORM-based persistence. Each repository wraps a `*gorm.DB`.
7. **`internal/types/interfaces/`** — all Go interfaces (service and repository contracts). This is the "ports" layer that infrastructure adapters implement.

### Async Task Pipeline (Asynq)

Three priority queues (critical:6, default:3, low:1). Task flow:

1. **`TypeDocumentProcess`** (`default` queue) — PDF uploaded → Python gRPC parses it → Go writes Markdown + Blocks → document status becomes `ready`.
2. **`TypePaperMetadataExtract`** (`default` queue) — On parse success, extracts title/authors/abstract/raw references via Python gRPC `PaperMetadataExtractor.Extract`.
3. **`TypePaperCatalogResolve`** (`low` queue) — If DOI or title exists after extraction, fans out to configured catalog providers (arXiv, Semantic Scholar, OpenAlex, Crossref) to enrich metadata.

`internal/router/tasks.go` registers task handlers, manages retry classification (gRPC status codes → skip/retry), and the dead-letter middleware that marks documents as `failed` when tasks exhaust retries.

### Error Handling

`internal/errors/errors.go` defines `ServerError` with error codes and HTTP statuses. Use the named constructors (`NewBadRequestError`, `NewNotFoundError`, `NewInternalServerError`, etc.). Do not expose wrapped/database errors to clients — the error middleware serializes only `ServerError`.

### Logging

`internal/logger/logger.go` provides a global `slog` singleton: `logger.Debug(ctx, ...)`, `logger.Info(ctx, ...)`, `logger.Warn(ctx, ...)`, `logger.Error(ctx, ...)`. Always pass `context.Context` as first argument. HTTP middleware injects a `request_id`. Use key-value pairs (`"document_id", id`), not printf placeholders. `LOG_LEVEL` filters output; `LOG_PATH` enables file rotation via lumberjack.

### Configuration

`internal/config/config.go` reads all settings from environment variables (with sensible defaults). `.env` is auto-loaded at startup. Environment variables take precedence over `.env`.

### Infrastructure Adapters

- **`docrecognition/`** — `default` (Go-native TXT/MD/JSON/CSV) and `builtin` (Python gRPC for PDF/Word/Excel/PPT)
- **`paperextraction/`** — gRPC client for Python metadata extraction
- **`papercatalog/`** — HTTP clients for arXiv, Semantic Scholar, OpenAlex, Crossref; fanned out concurrently via `registry.go`

### Data Model

- `documents` — source file lifecycle (status: uploaded→queued→processing→ready/failed)
- `paper_metadata` — bibliography (title, authors, abstract, tags, venue)
- `paper_citations` — citation relationships between papers
- `document_blocks` — per-page parsed PDF blocks with coordinates and Markdown
- `document_bases` / `document_base_files` — named paper collections (many-to-many)

### Proto Contract

`proto/document_parser.proto` is the shared contract between Go and Python. Generated Go stubs live in `gen/papers/v1/`; Python stubs are `python/document_parser_pb2*.py`. Never hand-edit generated files — regenerate from proto using commands in `python/README.md`.

### Frontend

React + TypeScript + Vite in `web/src/`. Main components: `PdfReader.tsx` (PDF.js via react-pdf), `PaperInfo.css` (new paper info panel). `api.ts` contains the typed API client. The Docker `web` service runs nginx serving the built assets and reverse-proxying `/api` to the Go API.

### Plan Tracking

Active multi-phase work is tracked in `.codex_plan`. Read it before starting repository work. Mark phases complete only after code AND verification pass. Update the plan after material changes.
