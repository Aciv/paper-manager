-- Reading notes may be deliberately empty (a blank Markdown starting point)
-- and the whole-note ceiling is now 60 MiB. Inline semantic edits stay small;
-- large content uses the raw section/file endpoints.
ALTER TABLE document_reading_notes
    DROP CONSTRAINT IF EXISTS chk_document_reading_notes_size;

ALTER TABLE document_reading_notes
    ADD CONSTRAINT chk_document_reading_notes_size
    CHECK (size_bytes >= 0 AND size_bytes <= 62914560);
