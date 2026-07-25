DROP TABLE IF EXISTS document_base_files;
DROP TABLE IF EXISTS document_bases;
DROP TABLE IF EXISTS paper_metadata;
ALTER TABLE documents RENAME COLUMN original_filename TO filename;
