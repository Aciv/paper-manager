-- Distinguish user-managed citation edges from extractor-managed edges.
-- Historical rows have no edge-level provenance, so mark them manual to
-- preserve data during upgrade. Users can explicitly replace/remove them.
ALTER TABLE paper_citations
    ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'manual';

ALTER TABLE paper_citations
    DROP CONSTRAINT IF EXISTS chk_paper_citations_origin;

ALTER TABLE paper_citations
    ADD CONSTRAINT chk_paper_citations_origin
    CHECK (origin IN ('manual', 'extraction'));

CREATE INDEX IF NOT EXISTS idx_paper_citations_automatic_owner
    ON paper_citations (citing_document_id, origin, created_at);
