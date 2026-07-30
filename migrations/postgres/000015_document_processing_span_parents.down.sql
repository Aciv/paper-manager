DROP INDEX IF EXISTS idx_document_processing_spans_parent_active;
ALTER TABLE document_processing_spans
    DROP COLUMN IF EXISTS parent_span_id;
