DO $$
BEGIN
    IF to_regclass(current_schema() || '.document_base_files') IS NOT NULL
       AND to_regclass(current_schema() || '.document_base_documents') IS NULL THEN
        ALTER TABLE document_base_files RENAME TO document_base_documents;
    END IF;
END $$;

ALTER INDEX IF EXISTS idx_document_base_files_document_id
    RENAME TO idx_document_base_documents_document_id;
ALTER INDEX IF EXISTS idx_document_base_files_base_order
    RENAME TO idx_document_base_documents_base_order;
