-- A reference belongs to the paper whose bibliography contains it. Rebuild
-- paper_citations as that single durable row and merge the shared
-- paper_references metadata into it. cited_document_id is now the only
-- resolution result; there is no separate resolved_document_id to keep in
-- sync across citing papers.

CREATE TABLE paper_citations_next (
    id UUID PRIMARY KEY,
    citing_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    reference_key TEXT NOT NULL,
    raw_reference TEXT NOT NULL,
    doi TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    authors JSONB NOT NULL DEFAULT '[]'::jsonb,
    venue TEXT NOT NULL DEFAULT '',
    published_at DATE,
    volume TEXT NOT NULL DEFAULT '',
    issue TEXT NOT NULL DEFAULT '',
    pages TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    metadata_source TEXT NOT NULL DEFAULT 'unknown',
    resolution_status TEXT NOT NULL DEFAULT 'not_started',
    resolution_generation BIGINT NOT NULL DEFAULT 0,
    resolution_provider TEXT NOT NULL DEFAULT '',
    resolution_error_code TEXT,
    resolution_error_message TEXT,
    resolution_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cited_document_id UUID REFERENCES documents(id) ON DELETE SET NULL,
    origin TEXT NOT NULL DEFAULT 'manual',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_paper_citations_authors_array CHECK (jsonb_typeof(authors) = 'array'),
    CONSTRAINT chk_paper_citations_metadata_source CHECK (
        metadata_source IN ('unknown', 'extraction', 'catalog', 'manual')
    ),
    CONSTRAINT chk_paper_citations_status CHECK (
        resolution_status IN (
            'not_started', 'queued', 'running', 'succeeded', 'failed',
            'cancelled', 'skipped'
        )
    ),
    CONSTRAINT chk_paper_citations_generation CHECK (resolution_generation >= 0),
    CONSTRAINT chk_paper_citations_raw_bounds CHECK (length(raw_reference) <= 65536),
    CONSTRAINT chk_paper_citations_errors CHECK (
        length(coalesce(resolution_error_code, '')) <= 64
        AND length(coalesce(resolution_error_message, '')) <= 512
    ),
    CONSTRAINT chk_paper_citations_origin CHECK (origin IN ('manual', 'extraction')),
    CONSTRAINT chk_paper_citations_not_self CHECK (
        cited_document_id IS NULL OR citing_document_id <> cited_document_id
    ),
    CONSTRAINT uq_paper_citations_owner_reference UNIQUE (citing_document_id, reference_key)
);

INSERT INTO paper_citations_next (
    id,
    citing_document_id,
    reference_key,
    raw_reference,
    doi,
    title,
    authors,
    venue,
    published_at,
    volume,
    issue,
    pages,
    source_url,
    metadata_source,
    resolution_status,
    resolution_generation,
    resolution_provider,
    resolution_error_code,
    resolution_error_message,
    resolution_updated_at,
    cited_document_id,
    origin,
    created_at,
    updated_at
)
SELECT
    gen_random_uuid(),
    c.citing_document_id,
    lower(regexp_replace(btrim(c.raw_reference), '[[:space:]]+', ' ', 'g')),
    c.raw_reference,
    r.doi,
    r.title,
    r.authors,
    r.venue,
    r.published_at,
    r.volume,
    r.issue,
    r.pages,
    r.source_url,
    r.metadata_source,
    CASE
        WHEN COALESCE(c.cited_document_id, r.resolved_document_id) = c.citing_document_id
            THEN 'not_started'
        ELSE r.resolution_status
    END,
    r.resolution_generation,
    r.resolution_provider,
    r.resolution_error_code,
    r.resolution_error_message,
    r.resolution_updated_at,
    CASE
        WHEN COALESCE(c.cited_document_id, r.resolved_document_id) = c.citing_document_id
            THEN NULL
        ELSE COALESCE(c.cited_document_id, r.resolved_document_id)
    END,
    c.origin,
    c.created_at,
    r.updated_at
FROM paper_citations c
JOIN paper_references r ON r.id = c.reference_id;

DROP TABLE paper_citations;
ALTER TABLE paper_citations_next RENAME TO paper_citations;

CREATE INDEX IF NOT EXISTS idx_paper_citations_citing_document
    ON paper_citations (citing_document_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_paper_citations_cited_document_id
    ON paper_citations (cited_document_id)
    WHERE cited_document_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_paper_citations_resolution_active
    ON paper_citations (resolution_updated_at, id)
    WHERE resolution_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_paper_citations_doi
    ON paper_citations (doi)
    WHERE doi <> '';

DROP TABLE paper_references;
