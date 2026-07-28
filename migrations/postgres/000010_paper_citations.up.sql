-- Store directional citation edges. A cited document may be an unknown
-- bibliographic placeholder until catalog resolution finds a stored paper;
-- raw_reference preserves the exact extraction evidence throughout.
CREATE TABLE IF NOT EXISTS paper_citations (
    citing_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    cited_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    raw_reference TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (citing_document_id, cited_document_id),
    CONSTRAINT chk_paper_citations_not_self
        CHECK (citing_document_id <> cited_document_id)
);

-- The primary key supports outgoing-reference queries. This reverse index
-- supports "which stored papers cite this document?" queries.
CREATE INDEX IF NOT EXISTS idx_paper_citations_cited_document_id
    ON paper_citations (cited_document_id, created_at DESC);
