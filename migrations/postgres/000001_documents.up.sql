-- Document metadata and parsed-content schema.
-- Applied by migrations/postgres.Migrate during API startup.

CREATE TABLE IF NOT EXISTS documents (
    id UUID PRIMARY KEY,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    doi TEXT NOT NULL DEFAULT '',
    storage_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'uploaded'
        CONSTRAINT chk_documents_status
        CHECK (status IN ('uploaded', 'queued', 'processing', 'ready', 'failed')),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_documents_doi ON documents (doi) WHERE doi <> '';
CREATE INDEX IF NOT EXISTS idx_documents_created_at ON documents (created_at DESC);

CREATE TABLE IF NOT EXISTS document_blocks (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    order_index INTEGER NOT NULL,
    type TEXT NOT NULL,
    text TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '',
    markdown_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    page_number INTEGER NOT NULL CHECK (page_number >= 1),
    page_width DOUBLE PRECISION NOT NULL CHECK (page_width > 0),
    page_height DOUBLE PRECISION NOT NULL CHECK (page_height > 0),
    bbox_x0 DOUBLE PRECISION NOT NULL,
    bbox_y0 DOUBLE PRECISION NOT NULL,
    bbox_x1 DOUBLE PRECISION NOT NULL,
    bbox_y1 DOUBLE PRECISION NOT NULL,
    markdown_codepoint_start INTEGER NOT NULL CHECK (markdown_codepoint_start >= 0),
    markdown_codepoint_end INTEGER NOT NULL CHECK (markdown_codepoint_end >= markdown_codepoint_start),
    CONSTRAINT uq_document_blocks_document_order UNIQUE (document_id, order_index),
    CONSTRAINT chk_document_blocks_asset_url
        CHECK (type NOT IN ('image', 'table') OR url <> ''),
    CONSTRAINT chk_document_blocks_bbox
        CHECK (bbox_x0 >= 0 AND bbox_y0 >= 0
            AND bbox_x1 > bbox_x0 AND bbox_y1 > bbox_y0
            AND bbox_x1 <= page_width AND bbox_y1 <= page_height)
);
CREATE INDEX IF NOT EXISTS idx_document_blocks_page
    ON document_blocks (document_id, page_number, order_index);

-- Permanently failed asynchronous tasks. Payload is retained for diagnosis
-- and optional replay; document identifiers are nullable for generic tasks.
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
CREATE INDEX IF NOT EXISTS idx_task_dead_letters_task_type ON task_dead_letters(task_type);
