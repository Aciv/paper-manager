ALTER TABLE document_markdowns
    DROP CONSTRAINT IF EXISTS chk_document_markdowns_outline_object,
    DROP CONSTRAINT IF EXISTS chk_document_markdowns_recognition_generation,
    DROP COLUMN IF EXISTS recognition_generation,
    DROP COLUMN IF EXISTS document_outline;
