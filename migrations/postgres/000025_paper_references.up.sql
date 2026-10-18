-- Breaking cutover: unresolved references become first-class rows instead of
-- documents placeholders. The old citation table cannot represent a reference
-- identity independent of a cited document, so it is rebuilt. This migration
-- intentionally does not backfill historical placeholder metadata; deployments
-- upgrading this pre-release schema must reset their test data first.

DROP TABLE IF EXISTS paper_citations;

DELETE FROM documents WHERE source_state = 'missing';

ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_source_state;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_available_storage;

ALTER TABLE documents
    ADD CONSTRAINT chk_documents_source_state
        CHECK (source_state IN ('available', 'deleting', 'merged')),
    ADD CONSTRAINT chk_documents_available_storage
        CHECK (
            source_state <> 'available'
            OR (storage_key <> '' AND storage_key NOT LIKE 'unknown://%')
        );

-- A canonical document owns its merged tombstones. Deleting it must not be
-- blocked by a self-referencing child row.
ALTER TABLE documents DROP CONSTRAINT IF EXISTS fk_documents_canonical_document;
ALTER TABLE documents
    ADD CONSTRAINT fk_documents_canonical_document
        FOREIGN KEY (canonical_document_id) REFERENCES documents(id) ON DELETE CASCADE;

CREATE TABLE IF NOT EXISTS paper_references (
    id UUID PRIMARY KEY,
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
    raw_reference TEXT NOT NULL DEFAULT '',
    resolution_status TEXT NOT NULL DEFAULT 'not_started',
    resolution_generation BIGINT NOT NULL DEFAULT 0,
    resolution_provider TEXT NOT NULL DEFAULT '',
    resolution_error_code TEXT,
    resolution_error_message TEXT,
    resolution_updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_document_id UUID REFERENCES documents(id) ON DELETE SET NULL,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_paper_references_authors_array CHECK (jsonb_typeof(authors) = 'array'),
    CONSTRAINT chk_paper_references_metadata_source CHECK (
        metadata_source IN ('unknown', 'extraction', 'catalog', 'manual')
    ),
    CONSTRAINT chk_paper_references_status CHECK (
        resolution_status IN (
            'not_started', 'queued', 'running', 'succeeded', 'failed',
            'cancelled', 'skipped'
        )
    ),
    CONSTRAINT chk_paper_references_generation CHECK (resolution_generation >= 0),
    CONSTRAINT chk_paper_references_raw_bounds CHECK (length(raw_reference) <= 65536),
    CONSTRAINT chk_paper_references_errors CHECK (
        length(coalesce(resolution_error_code, '')) <= 64
        AND length(coalesce(resolution_error_message, '')) <= 512
    )
);

CREATE INDEX IF NOT EXISTS idx_paper_references_resolved_document_id
    ON paper_references (resolved_document_id)
    WHERE resolved_document_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_paper_references_resolution_active
    ON paper_references (resolution_updated_at, id)
    WHERE resolution_status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS idx_paper_references_doi
    ON paper_references (doi)
    WHERE doi <> '';

CREATE TABLE IF NOT EXISTS paper_citations (
    citing_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    reference_id UUID NOT NULL REFERENCES paper_references(id) ON DELETE CASCADE,
    cited_document_id UUID REFERENCES documents(id) ON DELETE SET NULL,
    raw_reference TEXT NOT NULL,
    origin TEXT NOT NULL DEFAULT 'manual',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (citing_document_id, reference_id),
    CONSTRAINT chk_paper_citations_origin CHECK (origin IN ('manual', 'extraction')),
    CONSTRAINT chk_paper_citations_not_self
        CHECK (cited_document_id IS NULL OR citing_document_id <> cited_document_id)
);

CREATE INDEX IF NOT EXISTS idx_paper_citations_reference_id
    ON paper_citations (reference_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_paper_citations_cited_document_id
    ON paper_citations (cited_document_id)
    WHERE cited_document_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_paper_citations_automatic_owner
    ON paper_citations (citing_document_id, origin, created_at);
