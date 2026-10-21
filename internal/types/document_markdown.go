package types

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const MaxDocumentMarkdownBytes = 32 << 20

var ErrDocumentMarkdownTooLarge = errors.New("document Markdown exceeds the read limit")

// DocumentMarkdown records the durable Markdown source produced for a
// document's current successful parse. The Markdown bytes themselves live in
// FileStorage under StorageKey; blocks are derived, query-oriented data and
// can be rebuilt from that source. Reprocessing replaces this single row and
// its stored Markdown object rather than retaining unbounded parse history.
type DocumentMarkdown struct {
	DocumentID            uuid.UUID `gorm:"column:document_id;type:uuid;primaryKey" json:"documentId"`
	StorageKey            string    `gorm:"column:storage_key;type:text;not null;uniqueIndex" json:"-"`
	Filename              string    `gorm:"column:filename;type:text;not null" json:"filename"`
	ContentType           string    `gorm:"column:content_type;type:text;not null;default:text/markdown" json:"contentType"`
	SizeBytes             int64     `gorm:"column:size_bytes;not null;check:size_bytes >= 0" json:"sizeBytes"`
	ParserName            string    `gorm:"column:parser_name;type:text;not null;default:''" json:"parserName,omitempty"`
	ParserVersion         string    `gorm:"column:parser_version;type:text;not null;default:''" json:"parserVersion,omitempty"`
	InputMode             string    `gorm:"column:input_mode;type:text;not null;default:''" json:"inputMode,omitempty"`
	Language              string    `gorm:"column:language;type:varchar(16);not null;default:''" json:"language,omitempty"`
	PageCount             int       `gorm:"column:page_count;not null;default:0;check:page_count >= 0" json:"pageCount,omitempty"`
	Metadata              JSON      `gorm:"column:metadata;type:jsonb;serializer:json;not null" json:"metadata,omitempty"`
	DocumentOutline       JSON      `gorm:"column:document_outline;type:jsonb;serializer:json;not null" json:"documentOutline"`
	RecognitionGeneration int64     `gorm:"column:recognition_generation;not null;default:0" json:"recognitionGeneration"`
	CreatedAt             time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt             time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (DocumentMarkdown) TableName() string { return "document_markdowns" }

// DocumentMarkdownContent is the explicit full-Markdown read result. The
// manifest remains the durable metadata; Markdown bytes are loaded only when a
// consumer requests the source document.
type DocumentMarkdownContent struct {
	DocumentID            uuid.UUID `json:"documentId"`
	Filename              string    `json:"filename"`
	ContentType           string    `json:"contentType"`
	SizeBytes             int64     `json:"sizeBytes"`
	PageCount             int       `json:"pageCount,omitempty"`
	RecognitionGeneration int64     `json:"recognitionGeneration"`
	Markdown              []byte    `json:"-"`
}
