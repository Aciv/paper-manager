-- Preserve the parent/child topology of asynchronous work. A reference
-- provider request may be a child of a source document's reference-run root,
-- so the parent link deliberately does not require equal document_id values.
ALTER TABLE document_processing_spans
    ADD COLUMN IF NOT EXISTS parent_span_id UUID NULL
        REFERENCES document_processing_spans(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_document_processing_spans_parent_active
    ON document_processing_spans (parent_span_id, status, updated_at DESC)
    WHERE parent_span_id IS NOT NULL
      AND status IN ('queued', 'running');
