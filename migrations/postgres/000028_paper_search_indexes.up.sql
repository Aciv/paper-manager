-- Paper search filters title and author with ILIKE '%term%'. A B-tree index
-- cannot serve an unanchored substring match, so enable pg_trgm and index the
-- trigram projections instead.
--
-- pg_trgm is database-wide. Integration tests and deployments with a custom
-- search_path may already have it installed in a schema that is not visible
-- from this migration's search_path, so resolve the extension schema and
-- schema-qualify the operator class when creating the indexes.
DO $$
DECLARE
    trgm_schema TEXT;
BEGIN
    SELECT namespace.nspname
    INTO trgm_schema
    FROM pg_extension extension
    JOIN pg_namespace namespace ON namespace.oid = extension.extnamespace
    WHERE extension.extname = 'pg_trgm';

    IF trgm_schema IS NULL THEN
        -- Pin the extension to public. Test schemas and custom search_path
        -- deployments must not own it: dropping such a schema would drop the
        -- operator class while indexes still depend on it.
        CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;
        SELECT namespace.nspname
        INTO trgm_schema
        FROM pg_extension extension
        JOIN pg_namespace namespace ON namespace.oid = extension.extnamespace
        WHERE extension.extname = 'pg_trgm';
    END IF;

    IF trgm_schema IS NULL THEN
        RAISE EXCEPTION 'pg_trgm extension is not available';
    END IF;

    IF trgm_schema <> 'public' THEN
        ALTER EXTENSION pg_trgm SET SCHEMA public;
        trgm_schema := 'public';
    END IF;

    EXECUTE format(
        'CREATE INDEX IF NOT EXISTS idx_paper_metadata_title_trgm
         ON paper_metadata USING GIN (title %I.gin_trgm_ops)',
        trgm_schema
    );
END $$;

-- Authors stay in the JSONB array for atomic display and updates, but a
-- normalized text projection makes them searchable. The trigger below owns the
-- projection so every writer (including migrations and direct SQL) stays
-- consistent; application code must never write author_names itself.
ALTER TABLE paper_metadata
    ADD COLUMN IF NOT EXISTS author_names TEXT NOT NULL DEFAULT '';

-- Newline is the projection delimiter: it cannot occur inside a normalized
-- author name, so a search term cannot accidentally span two authors.
UPDATE paper_metadata AS metadata
SET author_names = COALESCE((
    SELECT string_agg(author->>'name', E'\n' ORDER BY ordinality)
    FROM jsonb_array_elements(metadata.authors) WITH ORDINALITY AS item(author, ordinality)
    WHERE COALESCE(author->>'name', '') <> ''
), '')
WHERE metadata.authors <> '[]'::jsonb;

CREATE OR REPLACE FUNCTION paper_metadata_sync_author_names() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.author_names := COALESCE((
        SELECT string_agg(author->>'name', E'\n' ORDER BY ordinality)
        FROM jsonb_array_elements(NEW.authors) WITH ORDINALITY AS item(author, ordinality)
        WHERE COALESCE(author->>'name', '') <> ''
    ), '');
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_paper_metadata_sync_author_names ON paper_metadata;
CREATE TRIGGER trg_paper_metadata_sync_author_names
    BEFORE INSERT OR UPDATE OF authors ON paper_metadata
    FOR EACH ROW EXECUTE FUNCTION paper_metadata_sync_author_names();

DO $$
DECLARE
    trgm_schema TEXT;
BEGIN
    SELECT namespace.nspname
    INTO trgm_schema
    FROM pg_extension extension
    JOIN pg_namespace namespace ON namespace.oid = extension.extnamespace
    WHERE extension.extname = 'pg_trgm';

    EXECUTE format(
        'CREATE INDEX IF NOT EXISTS idx_paper_metadata_author_names_trgm
         ON paper_metadata USING GIN (author_names %I.gin_trgm_ops)',
        trgm_schema
    );
END $$;

-- Search results are ordered by this key. The document_id tie-breaker keeps
-- pagination deterministic when two rows share updated_at.
CREATE INDEX IF NOT EXISTS idx_paper_metadata_updated_at
    ON paper_metadata (updated_at DESC, document_id DESC);

-- idx_paper_metadata_published_at already exists from migration 4.
