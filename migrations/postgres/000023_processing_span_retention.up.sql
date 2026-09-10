-- Supports bounded, leaf-first deletion of terminal observation rows. The
-- current document stage/generation columns remain the execution fence.
CREATE INDEX IF NOT EXISTS idx_document_processing_spans_terminal_retention
    ON document_processing_spans (finished_at ASC, id ASC)
    WHERE status IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_document_processing_spans_parent_active
    ON document_processing_spans (parent_span_id)
    WHERE parent_span_id IS NOT NULL;
