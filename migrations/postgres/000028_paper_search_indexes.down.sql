-- Intentionally order-safe: indexes and the projection are dropped before the
-- extension they depend on. The down migration is documentation only and is
-- never executed by the application.
DROP INDEX IF EXISTS idx_paper_metadata_updated_at;
DROP INDEX IF EXISTS idx_paper_metadata_author_names_trgm;
DROP INDEX IF EXISTS idx_paper_metadata_title_trgm;

DROP TRIGGER IF EXISTS trg_paper_metadata_sync_author_names ON paper_metadata;
DROP FUNCTION IF EXISTS paper_metadata_sync_author_names();

ALTER TABLE paper_metadata DROP COLUMN IF EXISTS author_names;

DROP EXTENSION IF EXISTS pg_trgm;
