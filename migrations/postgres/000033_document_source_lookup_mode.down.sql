ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS chk_documents_source_lookup_mode,
    DROP COLUMN IF EXISTS source_lookup_mode;
