-- Markdown is a derived parse artifact. It is not a document identity, so
-- revisions are no longer hashed or carried through the asynchronous paper
-- information pipeline. Original upload bytes remain deduplicated by
-- documents.file_sha256.
ALTER TABLE document_markdowns DROP COLUMN IF EXISTS sha256;
ALTER TABLE document_blocks DROP COLUMN IF EXISTS markdown_sha256;
