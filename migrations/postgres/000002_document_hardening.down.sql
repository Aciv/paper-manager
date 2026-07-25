DROP INDEX IF EXISTS idx_task_dead_letters_scope_cursor;
DROP INDEX IF EXISTS uq_documents_doi_nonempty;

ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_status;
ALTER TABLE documents ADD CONSTRAINT chk_documents_status
    CHECK (status IN ('uploaded', 'queued', 'processing', 'ready', 'failed'));
