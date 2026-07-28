-- Unresolved catalog references are represented by placeholder documents so
-- citation edges retain two stable document IDs. The raw extraction text is
-- kept on the edge as evidence and for later re-resolution.
ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_status;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_status_check;
ALTER TABLE documents ADD CONSTRAINT chk_documents_status
    CHECK (status IN (
        'unknown', 'uploaded', 'queued', 'processing', 'cancelled',
        'deleting', 'ready', 'failed'
    ));

-- IF NOT EXISTS upgrades databases that already applied migration 000010.
-- The default preserves any pre-existing citation edges whose original
-- extraction result is no longer available.
ALTER TABLE paper_citations
    ADD COLUMN IF NOT EXISTS raw_reference TEXT NOT NULL DEFAULT '';
ALTER TABLE paper_citations
    ALTER COLUMN raw_reference DROP DEFAULT;
