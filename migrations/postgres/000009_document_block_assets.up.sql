-- Keep the storage provider key private while exposing a stable API URL on
-- image/table blocks. Legacy text-only rows retain the empty default.
ALTER TABLE document_blocks
    ADD COLUMN IF NOT EXISTS asset_key TEXT NOT NULL DEFAULT '';
