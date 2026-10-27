ALTER TABLE documents
    ADD COLUMN source_lookup_mode TEXT NOT NULL DEFAULT 'auto',
    ADD CONSTRAINT chk_documents_source_lookup_mode
        CHECK (source_lookup_mode IN ('auto', 'skip'));
