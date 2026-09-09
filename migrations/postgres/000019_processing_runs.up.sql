-- Separate durable workflow control from high-cardinality processing spans.
-- Existing spans remain readable with a NULL run_id during the rolling
-- migration; producers and workers are switched in a later phase.
CREATE TABLE IF NOT EXISTS processing_runs (
    id UUID PRIMARY KEY,
    owner_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    workflow_kind TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    attempt BIGINT NOT NULL DEFAULT 1 CHECK (attempt > 0),
    state TEXT NOT NULL
        CHECK (state IN ('queued', 'running', 'cancel_requested', 'succeeded', 'failed', 'cancelled')),
    cancel_requested_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    terminal_at TIMESTAMPTZ,
    error_code TEXT NOT NULL DEFAULT '',
    error_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_processing_runs_workflow_kind
        CHECK (char_length(workflow_kind) BETWEEN 1 AND 64),
    CONSTRAINT chk_processing_runs_idempotency_key
        CHECK (char_length(idempotency_key) BETWEEN 1 AND 512),
    CONSTRAINT chk_processing_runs_error_code
        CHECK (char_length(error_code) <= 128),
    CONSTRAINT chk_processing_runs_error_reason
        CHECK (char_length(error_reason) <= 512),
    CONSTRAINT uq_processing_runs_idempotency
        UNIQUE (owner_document_id, workflow_kind, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_processing_runs_active_owner_workflow
    ON processing_runs (owner_document_id, workflow_kind, updated_at DESC)
    WHERE state IN ('queued', 'running', 'cancel_requested');

CREATE INDEX IF NOT EXISTS idx_processing_runs_terminal_retention
    ON processing_runs (terminal_at, id)
    WHERE state IN ('succeeded', 'failed', 'cancelled');

-- Manual paper-info attempts currently use Unix nanoseconds, which exceed a
-- PostgreSQL INTEGER. Widen the legacy span column before those attempts are
-- associated with durable runs.
ALTER TABLE document_processing_spans
    ALTER COLUMN attempt TYPE BIGINT USING attempt::BIGINT;

ALTER TABLE document_processing_spans
    ADD COLUMN IF NOT EXISTS run_id UUID NULL;

ALTER TABLE document_processing_spans
    ADD CONSTRAINT fk_document_processing_spans_run
    FOREIGN KEY (run_id) REFERENCES processing_runs(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_document_processing_spans_run
    ON document_processing_spans (run_id, updated_at DESC)
    WHERE run_id IS NOT NULL;
