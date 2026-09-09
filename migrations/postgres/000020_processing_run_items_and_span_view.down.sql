DROP INDEX IF EXISTS idx_processing_runs_owner_view;
DROP INDEX IF EXISTS idx_document_processing_spans_current_view;
DROP INDEX IF EXISTS idx_processing_run_items_active;

ALTER TABLE processing_runs
    DROP COLUMN IF EXISTS items_sealed_at;

DROP TABLE IF EXISTS processing_run_items;
