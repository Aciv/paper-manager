-- pg_trgm is intentionally retained: it is database-wide and may have existed
-- before this application migration or be used by another schema.
DROP INDEX IF EXISTS idx_paper_metadata_updated_at;
DROP INDEX IF EXISTS idx_paper_metadata_author_names_trgm;
DROP INDEX IF EXISTS idx_paper_metadata_title_trgm;

DROP TRIGGER IF EXISTS trg_paper_metadata_sync_author_names ON paper_metadata;
DROP FUNCTION IF EXISTS paper_metadata_sync_author_names();

ALTER TABLE paper_metadata DROP COLUMN IF EXISTS author_names;
