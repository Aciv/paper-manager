ALTER TABLE paper_citations DROP COLUMN IF EXISTS raw_reference;

ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_status;
ALTER TABLE documents ADD CONSTRAINT chk_documents_status
    CHECK (status IN (
        'uploaded', 'queued', 'processing', 'cancelled',
        'deleting', 'ready', 'failed'
    ));
