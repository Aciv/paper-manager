-- Hardening changes for deployments that already applied migration 000001.

-- Early development deployments recorded version 1 before task dead-letter
-- persistence was added to that migration. Keep this migration self-contained
-- so those databases can upgrade instead of failing while creating its index.
CREATE TABLE IF NOT EXISTS task_dead_letters (
    id BIGSERIAL PRIMARY KEY,
    task_type VARCHAR(128) NOT NULL,
    scope VARCHAR(32) NOT NULL DEFAULT 'unknown',
    scope_id VARCHAR(128),
    related_id VARCHAR(128),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_error TEXT NOT NULL,
    fail_count INTEGER NOT NULL DEFAULT 1,
    failed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_status;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_status_check;
ALTER TABLE documents ADD CONSTRAINT chk_documents_status
    CHECK (status IN ('uploaded', 'queued', 'processing', 'deleting', 'ready', 'failed'));

-- A pre-flight DOI lookup is only an optimization. The database must enforce
-- uniqueness to close the race between concurrent uploads.
CREATE UNIQUE INDEX IF NOT EXISTS uq_documents_doi_nonempty
    ON documents (lower(doi)) WHERE doi <> '';

-- Matches dead-letter cursor pagination by scope.
CREATE INDEX IF NOT EXISTS idx_task_dead_letters_scope_cursor
    ON task_dead_letters (scope, scope_id, id DESC);
