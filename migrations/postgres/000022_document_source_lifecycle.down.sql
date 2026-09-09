DROP INDEX IF EXISTS idx_documents_canonical_document_id;
DROP INDEX IF EXISTS idx_documents_source_state_created;

ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS chk_documents_stage_error_bounds,
    DROP CONSTRAINT IF EXISTS chk_documents_available_storage,
    DROP CONSTRAINT IF EXISTS chk_documents_canonical_lifecycle,
    DROP CONSTRAINT IF EXISTS chk_documents_source_state,
    DROP CONSTRAINT IF EXISTS fk_documents_canonical_document,
    DROP COLUMN IF EXISTS reference_lookup_error_message,
    DROP COLUMN IF EXISTS reference_lookup_error_code,
    DROP COLUMN IF EXISTS source_lookup_error_message,
    DROP COLUMN IF EXISTS source_lookup_error_code,
    DROP COLUMN IF EXISTS metadata_extraction_error_message,
    DROP COLUMN IF EXISTS metadata_extraction_error_code,
    DROP COLUMN IF EXISTS markdown_error_message,
    DROP COLUMN IF EXISTS markdown_error_code,
    DROP COLUMN IF EXISTS canonical_document_id,
    DROP COLUMN IF EXISTS source_state;
