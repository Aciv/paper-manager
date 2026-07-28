-- Record which writer produced the current canonical metadata snapshot.
-- Existing rows are unknown because their historical writer cannot be
-- reconstructed safely.
ALTER TABLE paper_metadata
    ADD COLUMN IF NOT EXISTS metadata_source TEXT NOT NULL DEFAULT 'unknown';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'chk_paper_metadata_source'
          AND conrelid = 'paper_metadata'::regclass
    ) THEN
        ALTER TABLE paper_metadata
            ADD CONSTRAINT chk_paper_metadata_source
            CHECK (metadata_source IN ('unknown', 'extraction', 'catalog', 'manual'));
    END IF;
END $$;
