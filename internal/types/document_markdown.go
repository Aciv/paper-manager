package types

import (
	"time"

	"github.com/google/uuid"
)

// DocumentMarkdown records the durable Markdown source produced for a
// document's current successful parse. The Markdown bytes themselves live in
// FileStorage under StorageKey; blocks are derived, query-oriented data and
// can be rebuilt from that source. Reprocessing replaces this single row and
// its stored Markdown object rather than retaining unbounded parse history.
type DocumentMarkdown struct {
	DocumentID    uuid.UUID `gorm:"column:document_id;type:uuid;primaryKey" json:"documentId"`
	StorageKey    string    `gorm:"column:storage_key;type:text;not null;uniqueIndex" json:"-"`
	Filename      string    `gorm:"column:filename;type:text;not null" json:"filename"`
	ContentType   string    `gorm:"column:content_type;type:text;not null;default:text/markdown" json:"contentType"`
	SizeBytes     int64     `gorm:"column:size_bytes;not null;check:size_bytes >= 0" json:"sizeBytes"`
	SHA256        string    `gorm:"column:sha256;type:varchar(64);not null;default:''" json:"sha256,omitempty"`
	ParserName    string    `gorm:"column:parser_name;type:text;not null;default:''" json:"parserName,omitempty"`
	ParserVersion string    `gorm:"column:parser_version;type:text;not null;default:''" json:"parserVersion,omitempty"`
	InputMode     string    `gorm:"column:input_mode;type:text;not null;default:''" json:"inputMode,omitempty"`
	Language      string    `gorm:"column:language;type:varchar(16);not null;default:''" json:"language,omitempty"`
	PageCount     int       `gorm:"column:page_count;not null;default:0;check:page_count >= 0" json:"pageCount,omitempty"`
	Metadata      JSON      `gorm:"column:metadata;type:jsonb;serializer:json;not null" json:"metadata,omitempty"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (DocumentMarkdown) TableName() string { return "document_markdowns" }
