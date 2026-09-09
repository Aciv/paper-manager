DROP INDEX IF EXISTS idx_documents_reference_lookup_stage_active;
DROP INDEX IF EXISTS idx_documents_source_lookup_stage_active;
DROP INDEX IF EXISTS idx_documents_metadata_extraction_stage_active;
DROP INDEX IF EXISTS idx_documents_markdown_stage_active;

ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS chk_documents_reference_lookup_generation,
    DROP CONSTRAINT IF EXISTS chk_documents_source_lookup_generation,
    DROP CONSTRAINT IF EXISTS chk_documents_metadata_extraction_generation,
    DROP CONSTRAINT IF EXISTS chk_documents_markdown_generation,
    DROP CONSTRAINT IF EXISTS chk_documents_reference_lookup_status,
    DROP CONSTRAINT IF EXISTS chk_documents_source_lookup_status,
    DROP CONSTRAINT IF EXISTS chk_documents_metadata_extraction_status,
    DROP CONSTRAINT IF EXISTS chk_documents_markdown_status,
    DROP COLUMN IF EXISTS reference_lookup_updated_at,
    DROP COLUMN IF EXISTS reference_lookup_generation,
    DROP COLUMN IF EXISTS reference_lookup_status,
    DROP COLUMN IF EXISTS source_lookup_updated_at,
    DROP COLUMN IF EXISTS source_lookup_generation,
    DROP COLUMN IF EXISTS source_lookup_status,
    DROP COLUMN IF EXISTS metadata_extraction_updated_at,
    DROP COLUMN IF EXISTS metadata_extraction_generation,
    DROP COLUMN IF EXISTS metadata_extraction_status,
    DROP COLUMN IF EXISTS markdown_updated_at,
    DROP COLUMN IF EXISTS markdown_generation,
    DROP COLUMN IF EXISTS markdown_status;
