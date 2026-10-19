DROP INDEX IF EXISTS idx_paper_citations_bibliography_order;

ALTER TABLE paper_citations
    DROP CONSTRAINT IF EXISTS chk_paper_citations_bibliography_order,
    DROP COLUMN IF EXISTS bibliography_order;
