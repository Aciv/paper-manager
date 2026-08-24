package types

import (
	"github.com/google/uuid"
)

// Block is one semantically meaningful region of a PDF. It is the source-map
// record connecting an unmodified Markdown document to a rectangular region
// on one PDF page. Coordinates use the PyMuPDF page coordinate system
// (top-left origin, measured in PDF points), not browser pixels.
//
// A parser must populate the PDF-location fields for newly produced blocks.
// Pointer fields remain nullable solely so pre-source-map rows written by an
// older parser can be read and subsequently reprocessed safely.
type Block struct {
	ID         uuid.UUID `gorm:"column:id;type:uuid;primaryKey" json:"id"`
	DocumentID uuid.UUID `gorm:"column:document_id;type:uuid;not null;uniqueIndex:idx_document_block_order,priority:1;index:idx_document_blocks_page,priority:1" json:"documentId"`

	// Ordinal is the parser's stable reading order within the current Markdown
	// version. It lets the Markdown renderer attach data-block-id without
	// adding custom tags to the stored Markdown.
	Ordinal int    `gorm:"column:order_index;not null;uniqueIndex:idx_document_block_order,priority:2;index:idx_document_blocks_page,priority:3" json:"ordinal"`
	Type    string `gorm:"column:type;type:text;not null" json:"type"`
	Text    string `gorm:"column:text;type:text;not null" json:"text"`

	// URL stores the generated image asset for image or table blocks. Text is
	// the corresponding accessible description rather than the image payload.
	URL string `gorm:"column:url;type:text;not null;default:'';check:chk_document_blocks_asset_url,type NOT IN ('image','table') OR url <> ''" json:"url,omitempty"`
	// AssetKey is the private FileStorage key for URL. It is intentionally not
	// exposed to API clients; the block-image endpoint authorizes access before
	// opening this object.
	AssetKey string `gorm:"column:asset_key;type:text;not null;default:''" json:"-"`

	// Location is omitted for legacy blocks that predate PDF source mapping.
	// GORM embeds its typed fields into document_blocks, while the API exposes
	// them as one coherent `location` object.
	Location *PDFBlockLocation `gorm:"embedded" json:"location,omitempty"`
}

func (Block) TableName() string { return "document_blocks" }

// PDFBlockLocation identifies the exact source region for a Block. All
// coordinates are in the PyMuPDF page coordinate system: top-left origin and
// PDF-point units. Page dimensions are stored with the rectangle so clients
// can convert it to normalized coordinates before rendering at any zoom.
//
// MarkdownCodepointStart and MarkdownCodepointEnd are half-open Unicode
// code-point offsets in the complete Markdown file: [start, end). They are
// deliberately not Go byte offsets because page_boxes.pos originates from a
// Python string. Block ID and Ordinal remain the primary UI linkage.
type PDFBlockLocation struct {
	PageNumber *int     `gorm:"column:page_number;index:idx_document_blocks_page,priority:2" json:"pageNumber,omitempty"`
	PageWidth  *float64 `gorm:"column:page_width;type:double precision" json:"pageWidth,omitempty"`
	PageHeight *float64 `gorm:"column:page_height;type:double precision" json:"pageHeight,omitempty"`
	BBoxX0     *float64 `gorm:"column:bbox_x0;type:double precision" json:"bboxX0,omitempty"`
	BBoxY0     *float64 `gorm:"column:bbox_y0;type:double precision" json:"bboxY0,omitempty"`
	BBoxX1     *float64 `gorm:"column:bbox_x1;type:double precision" json:"bboxX1,omitempty"`
	BBoxY1     *float64 `gorm:"column:bbox_y1;type:double precision" json:"bboxY1,omitempty"`

	MarkdownCodepointStart *int `gorm:"column:markdown_codepoint_start" json:"markdownCodepointStart,omitempty"`
	MarkdownCodepointEnd   *int `gorm:"column:markdown_codepoint_end" json:"markdownCodepointEnd,omitempty"`
}
