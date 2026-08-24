-- Make document_blocks a PDF-to-Markdown source map. New fields are nullable
-- here to preserve blocks produced before coordinate capture was introduced;
-- a reparse replaces those legacy rows with fully located PDF blocks.

ALTER TABLE document_blocks
    ADD COLUMN IF NOT EXISTS page_width DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS page_height DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS bbox_x0 DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS bbox_y0 DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS bbox_x1 DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS bbox_y1 DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS markdown_codepoint_start INTEGER,
    ADD COLUMN IF NOT EXISTS markdown_codepoint_end INTEGER;

-- bbox and source_anchor were an untyped transitional representation. Keep
-- them for legacy rows until a subsequent reparse replaces the derived block
-- set; current code exclusively reads and writes the typed columns above.
CREATE INDEX IF NOT EXISTS idx_document_blocks_page
    ON document_blocks (document_id, page_number, order_index);
