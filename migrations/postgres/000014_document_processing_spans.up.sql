CREATE TABLE IF NOT EXISTS document_processing_spans (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    kind TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'document',
    scope_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    error_code TEXT NOT NULL DEFAULT '',
    error_reason TEXT NOT NULL DEFAULT '',
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_document_processing_spans_attempt_kind_scope
        UNIQUE (document_id, attempt, kind, scope, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_document_processing_spans_document_attempt
    ON document_processing_spans (document_id, attempt DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_document_processing_spans_active
    ON document_processing_spans (document_id, status, updated_at DESC)
    WHERE status IN ('queued', 'running');
