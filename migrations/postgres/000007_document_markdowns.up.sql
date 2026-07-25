-- Persist the current parser-produced Markdown source separately from its
-- derived document_blocks. Markdown bytes live in configured FileStorage;
-- this row is the durable database manifest for that object.
CREATE TABLE IF NOT EXISTS document_markdowns (
    document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL UNIQUE,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'text/markdown',
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 VARCHAR(64) NOT NULL DEFAULT '',
    parser_name TEXT NOT NULL DEFAULT '',
    parser_version TEXT NOT NULL DEFAULT '',
    input_mode TEXT NOT NULL DEFAULT '',
    language VARCHAR(16) NOT NULL DEFAULT '',
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_document_markdowns_content_type
        CHECK (content_type = 'text/markdown')
);
