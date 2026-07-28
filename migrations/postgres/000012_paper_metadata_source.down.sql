ALTER TABLE paper_metadata
    DROP CONSTRAINT IF EXISTS chk_paper_metadata_source;

ALTER TABLE paper_metadata
    DROP COLUMN IF EXISTS metadata_source;
