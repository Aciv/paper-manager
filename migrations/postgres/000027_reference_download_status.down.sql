ALTER TABLE paper_citations
    DROP CONSTRAINT IF EXISTS chk_paper_citations_download_errors;

ALTER TABLE paper_citations
    DROP COLUMN IF EXISTS download_updated_at,
    DROP COLUMN IF EXISTS download_error_message,
    DROP COLUMN IF EXISTS download_error_code,
    DROP COLUMN IF EXISTS download_requested_at,
    DROP COLUMN IF EXISTS download_attempt;
