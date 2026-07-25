-- Upgrade databases that applied the earlier document_base_documents name.
DO $$
BEGIN
    IF to_regclass(current_schema() || '.document_base_documents') IS NOT NULL
       AND to_regclass(current_schema() || '.document_base_files') IS NULL THEN
        ALTER TABLE document_base_documents RENAME TO document_base_files;
    END IF;
END $$;

ALTER INDEX IF EXISTS idx_document_base_documents_document_id
    RENAME TO idx_document_base_files_document_id;
ALTER INDEX IF EXISTS idx_document_base_documents_base_order
    RENAME TO idx_document_base_files_base_order;
