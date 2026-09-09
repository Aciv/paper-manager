-- Separate durable source-file identity from the four workflow stages. The
-- legacy status/error_message columns remain until the Phase E data cleanup.
ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS source_state TEXT NOT NULL DEFAULT 'available',
    ADD COLUMN IF NOT EXISTS canonical_document_id UUID NULL,
    ADD COLUMN IF NOT EXISTS markdown_error_code TEXT NULL,
    ADD COLUMN IF NOT EXISTS markdown_error_message TEXT NULL,
    ADD COLUMN IF NOT EXISTS metadata_extraction_error_code TEXT NULL,
    ADD COLUMN IF NOT EXISTS metadata_extraction_error_message TEXT NULL,
    ADD COLUMN IF NOT EXISTS source_lookup_error_code TEXT NULL,
    ADD COLUMN IF NOT EXISTS source_lookup_error_message TEXT NULL,
    ADD COLUMN IF NOT EXISTS reference_lookup_error_code TEXT NULL,
    ADD COLUMN IF NOT EXISTS reference_lookup_error_message TEXT NULL;

UPDATE documents
SET source_state = CASE
    WHEN status = 'deleting' THEN 'deleting'
    WHEN status = 'unknown' OR storage_key LIKE 'unknown://%' THEN 'missing'
    ELSE 'available'
END;

ALTER TABLE documents
    ADD CONSTRAINT fk_documents_canonical_document
        FOREIGN KEY (canonical_document_id) REFERENCES documents(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_documents_source_state
        CHECK (source_state IN ('missing', 'available', 'deleting', 'merged')),
    ADD CONSTRAINT chk_documents_canonical_lifecycle
        CHECK (
            (source_state = 'merged' AND canonical_document_id IS NOT NULL AND canonical_document_id <> id)
            OR (source_state <> 'merged' AND canonical_document_id IS NULL)
        ),
    ADD CONSTRAINT chk_documents_available_storage
        CHECK (source_state <> 'available' OR (storage_key <> '' AND storage_key NOT LIKE 'unknown://%')),
    ADD CONSTRAINT chk_documents_stage_error_bounds
        CHECK (
            length(COALESCE(markdown_error_code, '')) <= 64 AND
            length(COALESCE(markdown_error_message, '')) <= 512 AND
            length(COALESCE(metadata_extraction_error_code, '')) <= 64 AND
            length(COALESCE(metadata_extraction_error_message, '')) <= 512 AND
            length(COALESCE(source_lookup_error_code, '')) <= 64 AND
            length(COALESCE(source_lookup_error_message, '')) <= 512 AND
            length(COALESCE(reference_lookup_error_code, '')) <= 64 AND
            length(COALESCE(reference_lookup_error_message, '')) <= 512
        );

CREATE INDEX IF NOT EXISTS idx_documents_source_state_created
    ON documents (source_state, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_documents_canonical_document_id
    ON documents (canonical_document_id)
    WHERE canonical_document_id IS NOT NULL;
