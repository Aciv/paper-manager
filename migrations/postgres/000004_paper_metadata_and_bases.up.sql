-- Separate source-file handling from paper bibliographic data and collections.
-- Existing deployments retain their uploaded filename under the clearer name
-- original_filename; source document identity and storage keys are unchanged.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'documents'
          AND column_name = 'filename'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'documents'
          AND column_name = 'original_filename'
    ) THEN
        ALTER TABLE documents RENAME COLUMN filename TO original_filename;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS paper_metadata (
    document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    abstract TEXT NOT NULL DEFAULT '',
    authors JSONB NOT NULL DEFAULT '[]'::jsonb,
    tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    source_name TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    venue TEXT NOT NULL DEFAULT '',
    published_at DATE,
    volume TEXT NOT NULL DEFAULT '',
    issue TEXT NOT NULL DEFAULT '',
    pages TEXT NOT NULL DEFAULT '',
    language VARCHAR(16) NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_paper_metadata_authors_array CHECK (jsonb_typeof(authors) = 'array'),
    CONSTRAINT chk_paper_metadata_tags_array CHECK (jsonb_typeof(tags) = 'array')
);

-- A metadata row is present for legacy and newly uploaded documents, even
-- before a parser or user has identified the paper title.
INSERT INTO paper_metadata (document_id)
SELECT id FROM documents
ON CONFLICT (document_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_paper_metadata_published_at
    ON paper_metadata (published_at DESC);

CREATE TABLE IF NOT EXISTS document_bases (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_document_bases_name_lower
    ON document_bases (lower(name));

CREATE TABLE IF NOT EXISTS document_base_files (
    document_base_id UUID NOT NULL REFERENCES document_bases(id) ON DELETE CASCADE,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sort_order INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (document_base_id, document_id)
);

CREATE INDEX IF NOT EXISTS idx_document_base_files_document_id
    ON document_base_files (document_id);
CREATE INDEX IF NOT EXISTS idx_document_base_files_base_order
    ON document_base_files (document_base_id, sort_order, added_at DESC);
