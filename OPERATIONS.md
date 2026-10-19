# Processing and Remote Import Operations

## Runtime Topology

PostgreSQL stores documents, metadata, citations, dead letters, and processing
spans. Redis stores Asynq queues plus shared provider/download limiter state.
With `REDIS_ADDR` configured, provider/download concurrency, spacing, and
cooldown are shared across processes. The catalog dispatcher's queue and
provider assignment state are intentionally process-local: run a single API
process, and expect queued/running catalog work to be failed on restart.

The `critical`, `default`, and `low` queues handle application work. Remote PDF
acquisition uses the isolated `download` queue so slow hosts cannot consume all
parser/catalog capacity. `ASYNQ_CONCURRENCY` controls the total worker pool;
queue weights are configured in `internal/router/tasks.go`.

On graceful shutdown the catalog dispatcher drains its in-memory queue and
persists remaining source/reference attempts as failed before the database
closes. A hard crash still relies on the startup reconciliation sweep.

The catalog task names changed with the dispatcher cutover
(`paper:source_query_enqueue`, `paper:reference_query_enqueue`,
`paper:query_apply`). Tasks queued under the old names have no handler; after
deploying, report and remove them with `go run ./cmd/asynq-maintenance -purge`.
The startup sweep also fails the corresponding durable catalog rows.

Docker builds use `https://proxy.golang.org,direct` for Go modules by default.
If that endpoint is unavailable in the deployment network, select a reachable
mirror without editing the image definition:

```bash
GOPROXY=https://goproxy.cn,direct docker compose up --build -d
```

Host ports retain the documented defaults but can be isolated when another
development stack already owns them:

```bash
POSTGRES_HOST_PORT=15432 REDIS_HOST_PORT=16379 \
API_HOST_PORT=18080 WEB_HOST_PORT=15173 docker compose up -d
```

## Catalog Controls

`PAPER_CATALOG_PROVIDERS` enables adapters. Shared `PAPER_CATALOG_TIMEOUT` is a
fallback; `PAPER_CATALOG_<PROVIDER>_TIMEOUT`, `_SPACING`, and `_CONCURRENCY`
override it. Provider names are `ARXIV`, `SEMANTIC_SCHOLAR`, `OPENALEX`, and
`CROSSREF`. Spacing is the minimum interval between starts, while concurrency
limits simultaneous requests made through the shared provider limiter.

The in-process catalog dispatcher runs one worker per provider. Source jobs are
taken before reference jobs; reference admission is bounded by
`PAPER_REFERENCE_CATALOG_CONCURRENCY` and
`PAPER_REFERENCE_CATALOG_PER_DOCUMENT_CONCURRENCY`, leaving provider slots for
source lookups. Each job records its own tried-provider set and falls back to
the next provider after a failure or definitive no-match. Source lookup stops
early on an exact DOI/arXiv match or a score at or above
`PAPER_CATALOG_SOURCE_STOP_SCORE`.

`PAPER_EXTRACTION_TASK_TIMEOUT`/`_MAX_RETRY` and
`PAPER_CATALOG_TASK_TIMEOUT`/`_MAX_RETRY` set dispatch/post-processing task
budgets; provider HTTP calls use the provider-specific timeout. Asynq's
exponential retry delay is the fallback. Provider `Retry-After` updates the
shared cooldown and the dispatcher's per-provider cooling wait. The dispatcher
queue is process-local: run one API process and expect interrupted catalog rows
to be failed on restart. API keys remain optional for low-volume use:
`SEMANTIC_SCHOLAR_API_KEY`, `OPENALEX_API_KEY`, and `CROSSREF_MAILTO`.

## Remote PDF Controls

`REMOTE_PDF_DOWNLOAD_TIMEOUT` bounds one HTTP download and
`REMOTE_PDF_MAX_REDIRECTS` bounds redirect hops. Every target and redirect is
SSRF-validated. `FILE_MAX_UPLOAD_SIZE` is also the maximum remote response
size. PDF bytes must pass signature/content checks before atomic persistence.

`REMOTE_PDF_DOWNLOAD_CONCURRENCY` is the global download lease;
`REMOTE_PDF_HOST_CONCURRENCY` and `REMOTE_PDF_HOST_SPACING` protect each final
host. `REMOTE_PDF_TASK_TIMEOUT`/`_MAX_RETRY` bound one acquisition task, and
`REMOTE_PDF_DISPATCH_LIMIT` caps one bibliography run. A host `Retry-After`
updates shared cooldown state. Generic publisher pages are not scraped: supply
an explicit PDF URL or a supported arXiv/DOI URL.

## Status, Retry, and Cancellation

Read all user-visible progress from:

```bash
curl http://localhost:8080/api/document/<document-id>/processing
```

Document recognition may be cancelled/retried through `/api/document/<id>`
`/cancel` and `/retry`. Paper operations have independent retry endpoints:

```text
POST /api/paper/<id>/info/extraction/retry
POST /api/paper/<id>/info/catalog/retry
POST /api/paper/<id>/info/reference/retry
```

Reference catalog cancellation does not stop extraction/source lookup.
Reference PDF acquisition is requested per citation with
`POST /api/references/{referenceID}/download-pdf`; already persisted target PDFs
are unchanged.

For failures, inspect structured logs by `request_id`/`document_id`, then query
`task_dead_letters` by `task_type`, `scope`, and `scope_id`. A failed document
and a failed paper-info/reference span are different signals: a document may
remain `ready` while optional catalog work fails.

## Validation

Start the complete stack, then run deterministic smoke checks:

```bash
docker compose up --build -d
./scripts/test_all_api.sh
```

The scripts create unique records and clean them up. Public integrations are
opt-in because remote APIs can rate-limit or fail independently:

```bash
RUN_LIVE_INTEGRATION_TESTS=1 ./scripts/test_remote_import_api.sh
PAPER_CATALOG_LIVE_TEST=1 go test ./internal/infrastructure/papercatalog \
  -run '^TestLivePaperCatalogProviders$' -v
```

Never place provider credentials in scripts, logs, or committed `.env` files.
Configuration is read at process startup, so restart API workers after changing
limits.

## Reference storage cutover

Migration 26 merges `paper_references` into `paper_citations`: every
bibliography row now belongs to one citing paper and `cited_document_id` is its
only resolution result. The migration backfills existing rows, so a data reset
is not required. If upgrading from a pre-migration-25 schema, apply migration
25 first; it intentionally does not backfill historical placeholder metadata.
For a test environment that still contains placeholders, stop the API/worker
and reset before startup:

```bash
# Dev containers used by make dev
docker exec -e PGPASSWORD=papers papers-manager-dev-postgres-1 \
  psql -U papers -d papers -c 'TRUNCATE TABLE documents CASCADE; TRUNCATE TABLE document_bases CASCADE; TRUNCATE TABLE task_dead_letters;'
docker exec papers-manager-dev-redis-1 redis-cli FLUSHDB

# Then start the API; AUTO_MIGRATE applies all versions in order.
```

Uploaded objects are not deleted by the SQL reset. Remove obsolete files from
`data/uploads/` separately only when you are sure no real document references
them. After migration, `documents.source_state` no longer contains `missing`;
unresolved references are `paper_citations` rows with `cited_document_id NULL`,
and `reference_lookup` remains `not_started` until the manual
`/api/paper/:id/info/reference/retry` action unless
`PAPER_REFERENCE_AUTO_RESOLVE=true`.
