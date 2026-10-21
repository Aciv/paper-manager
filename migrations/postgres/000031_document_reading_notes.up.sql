CREATE TABLE IF NOT EXISTS document_reading_notes (
    document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    structure JSONB NOT NULL,
    size_bytes BIGINT NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_document_reading_notes_revision CHECK (revision > 0),
    CONSTRAINT chk_document_reading_notes_size CHECK (size_bytes > 0 AND size_bytes <= 10485760),
    CONSTRAINT chk_document_reading_notes_structure CHECK (jsonb_typeof(structure) = 'object')
);
