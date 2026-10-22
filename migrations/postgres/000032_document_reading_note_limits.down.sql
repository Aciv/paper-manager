-- Down migrations are documentation only and are not executed automatically.
-- Empty notes or notes over 10 MiB must be removed/resized before this
-- constraint can be restored in a manual rollback.
ALTER TABLE document_reading_notes
    DROP CONSTRAINT IF EXISTS chk_document_reading_notes_size;

ALTER TABLE document_reading_notes
    ADD CONSTRAINT chk_document_reading_notes_size
    CHECK (size_bytes > 0 AND size_bytes <= 10485760);
