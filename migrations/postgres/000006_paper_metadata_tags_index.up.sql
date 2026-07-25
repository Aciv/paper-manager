-- Supports JSONB containment searches used by PaperMetadataRepository:
-- tags @> '["computer-science"]'::jsonb.
CREATE INDEX IF NOT EXISTS idx_paper_metadata_tags_gin
    ON paper_metadata USING GIN (tags jsonb_path_ops);
