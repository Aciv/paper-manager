-- Byte-identical uploaded source documents are rejected before parser work.
-- Historical rows remain blank and intentionally do not participate.
ALTER TABLE documents ADD COLUMN IF NOT EXISTS file_sha256 CHAR(64) NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_documents_file_sha256_nonempty
    ON documents (file_sha256) WHERE file_sha256 <> '';
