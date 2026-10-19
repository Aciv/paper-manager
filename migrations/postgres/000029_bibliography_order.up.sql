ALTER TABLE paper_citations
    ADD COLUMN IF NOT EXISTS bibliography_order INTEGER;

ALTER TABLE paper_citations
    DROP CONSTRAINT IF EXISTS chk_paper_citations_bibliography_order;

ALTER TABLE paper_citations
    ADD CONSTRAINT chk_paper_citations_bibliography_order
    CHECK (bibliography_order IS NULL OR bibliography_order >= 0);

CREATE INDEX IF NOT EXISTS idx_paper_citations_bibliography_order
    ON paper_citations (citing_document_id, bibliography_order, id)
    WHERE bibliography_order IS NOT NULL;
