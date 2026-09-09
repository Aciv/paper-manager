-- Keep reference-download fan-out completion in the durable run control plane.
-- Span rows remain an optional observation projection and may be retained for
-- a shorter period than these work-item records.
CREATE TABLE IF NOT EXISTS processing_run_items (
    run_id UUID NOT NULL REFERENCES processing_runs(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    error_code TEXT NOT NULL DEFAULT '',
    error_reason TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    terminal_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (run_id, item_key),
    CONSTRAINT chk_processing_run_items_key
        CHECK (char_length(item_key) BETWEEN 1 AND 512),
    CONSTRAINT chk_processing_run_items_error_code
        CHECK (char_length(error_code) <= 128),
    CONSTRAINT chk_processing_run_items_error_reason
        CHECK (char_length(error_reason) <= 512)
);

ALTER TABLE processing_runs
    ADD COLUMN IF NOT EXISTS items_sealed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_processing_run_items_active
    ON processing_run_items (run_id, state, updated_at DESC)
    WHERE state IN ('queued', 'running');

-- Supports the bounded latest-stage window query used by the processing view.
CREATE INDEX IF NOT EXISTS idx_document_processing_spans_current_view
    ON document_processing_spans
        (document_id, kind, scope, scope_id, attempt DESC, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_processing_runs_owner_view
    ON processing_runs (owner_document_id, updated_at DESC, id DESC);
