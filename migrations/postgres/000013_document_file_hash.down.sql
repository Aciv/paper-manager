DROP INDEX IF EXISTS uq_documents_file_sha256_nonempty;
ALTER TABLE documents DROP COLUMN IF EXISTS file_sha256;
