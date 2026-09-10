-- Breaking cutover: all workers use document stage + generation as the only
-- control plane. These historical aggregate columns and tables are no longer
-- read or written by runtime code. This migration is intentionally irreversible
-- because restoring them would require reconstructing deleted history.

DROP INDEX IF EXISTS idx_document_processing_spans_run;
ALTER TABLE document_processing_spans
    DROP CONSTRAINT IF EXISTS fk_document_processing_spans_run,
    DROP COLUMN IF EXISTS run_id;

DROP TABLE IF EXISTS processing_run_items;
DROP TABLE IF EXISTS processing_runs;

ALTER TABLE documents
    DROP CONSTRAINT IF EXISTS chk_documents_status,
    DROP CONSTRAINT IF EXISTS documents_status_check,
    DROP COLUMN IF EXISTS error_message,
    DROP COLUMN IF EXISTS status;
