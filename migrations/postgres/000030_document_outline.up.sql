-- Persist the parser-selected source outline beside the current Markdown
-- manifest. Existing rows remain readable as an explicit legacy state until
-- their next recognition generation replaces all parsed output atomically.
ALTER TABLE document_markdowns
    ADD COLUMN IF NOT EXISTS document_outline JSONB NOT NULL DEFAULT
        '{"schemaVersion":0,"source":"unavailable","sections":[],"diagnostics":["legacy_missing_outline"]}'::jsonb,
    ADD COLUMN IF NOT EXISTS recognition_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE document_markdowns
    DROP CONSTRAINT IF EXISTS chk_document_markdowns_recognition_generation;
ALTER TABLE document_markdowns
    ADD CONSTRAINT chk_document_markdowns_recognition_generation
        CHECK (recognition_generation >= 0);

ALTER TABLE document_markdowns
    DROP CONSTRAINT IF EXISTS chk_document_markdowns_outline_object;
ALTER TABLE document_markdowns
    ADD CONSTRAINT chk_document_markdowns_outline_object
        CHECK (jsonb_typeof(document_outline) = 'object');
