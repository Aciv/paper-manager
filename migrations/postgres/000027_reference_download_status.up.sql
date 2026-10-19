-- Download is an explicit action on one reference and happens before a
-- document exists. These columns record only the last attempt; success is
-- represented by cited_document_id, failure by download_error_*.
ALTER TABLE paper_citations
    ADD COLUMN IF NOT EXISTS download_attempt UUID,
    ADD COLUMN IF NOT EXISTS download_requested_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS download_error_code TEXT,
    ADD COLUMN IF NOT EXISTS download_error_message TEXT,
    ADD COLUMN IF NOT EXISTS download_updated_at TIMESTAMPTZ;

ALTER TABLE paper_citations
    ADD CONSTRAINT chk_paper_citations_download_errors CHECK (
        length(coalesce(download_error_code, '')) <= 64
        AND length(coalesce(download_error_message, '')) <= 512
    );
