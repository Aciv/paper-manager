DROP INDEX IF EXISTS idx_document_processing_spans_run;

ALTER TABLE document_processing_spans
    DROP CONSTRAINT IF EXISTS fk_document_processing_spans_run;

ALTER TABLE document_processing_spans
    DROP COLUMN IF EXISTS run_id;

DROP INDEX IF EXISTS idx_processing_runs_terminal_retention;
DROP INDEX IF EXISTS idx_processing_runs_active_owner_workflow;
DROP TABLE IF EXISTS processing_runs;

-- attempt intentionally remains BIGINT: narrowing it could fail or lose data
-- after a manual retry has stored a value larger than INTEGER.
