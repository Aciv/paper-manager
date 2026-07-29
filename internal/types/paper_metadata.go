package types

import (
	"time"

	"github.com/google/uuid"
)

// PaperAuthor is one author as supplied by metadata extraction or a user.
// Authors remain embedded until author-level identity and aggregation are
// required; this avoids premature global-author deduplication.
type PaperAuthor struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation,omitempty"`
}

// PaperMetadataSource identifies the writer of the most recent metadata
// update. It is provenance only: persistence is last-write-wins per non-nil
// field and does not impose a source-priority order.
type PaperMetadataSource string

const (
	PaperMetadataSourceUnknown    PaperMetadataSource = "unknown"
	PaperMetadataSourceExtraction PaperMetadataSource = "extraction"
	PaperMetadataSourceCatalog    PaperMetadataSource = "catalog"
	PaperMetadataSourceManual     PaperMetadataSource = "manual"
)

// IsValid reports whether source is a supported metadata origin.
func (source PaperMetadataSource) IsValid() bool {
	switch source {
	case PaperMetadataSourceUnknown,
		PaperMetadataSourceExtraction,
		PaperMetadataSourceCatalog,
		PaperMetadataSourceManual:
		return true
	default:
		return false
	}
}

// PaperMetadata stores bibliographic facts about a document. It intentionally
// excludes base-specific state such as reading progress and personal tags.
type PaperMetadata struct {
	DocumentID     uuid.UUID           `gorm:"column:document_id;type:uuid;primaryKey" json:"documentId"`
	Title          string              `gorm:"column:title;type:text;not null;default:''" json:"title,omitempty"`
	Abstract       string              `gorm:"column:abstract;type:text;not null;default:''" json:"abstract,omitempty"`
	Authors        []PaperAuthor       `gorm:"column:authors;type:jsonb;serializer:json;not null" json:"authors"`
	Tags           []string            `gorm:"column:tags;type:jsonb;serializer:json;not null" json:"tags"`
	SourceName     string              `gorm:"column:source_name;type:text;not null;default:''" json:"sourceName,omitempty"`
	SourceURL      string              `gorm:"column:source_url;type:text;not null;default:''" json:"sourceUrl,omitempty"`
	Venue          string              `gorm:"column:venue;type:text;not null;default:''" json:"venue,omitempty"`
	PublishedAt    *time.Time          `gorm:"column:published_at;type:date" json:"publishedAt,omitempty"`
	Volume         string              `gorm:"column:volume;type:text;not null;default:''" json:"volume,omitempty"`
	Issue          string              `gorm:"column:issue;type:text;not null;default:''" json:"issue,omitempty"`
	Pages          string              `gorm:"column:pages;type:text;not null;default:''" json:"pages,omitempty"`
	Language       string              `gorm:"column:language;type:varchar(16);not null;default:''" json:"language,omitempty"`
	Metadata       JSON                `gorm:"column:metadata;type:jsonb;serializer:json;not null" json:"metadata,omitempty"`
	MetadataSource PaperMetadataSource `gorm:"column:metadata_source;type:text;not null;default:'unknown'" json:"metadataSource"`
	CreatedAt      time.Time           `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt      time.Time           `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (PaperMetadata) TableName() string { return "paper_metadata" }

// PaperTagMode controls whether a tag search requires every requested tag or
// at least one requested tag.
type PaperTagMode string

const (
	PaperTagModeAll PaperTagMode = "all"
	PaperTagModeAny PaperTagMode = "any"
)

// PaperSearch describes bibliographic search. Tags are normalized to lower
// case by the service before reaching the JSONB-backed repository.
type PaperSearch struct {
	Title    string
	Author   string
	Tags     []string
	TagMode  PaperTagMode
	Page     int
	PageSize int
}

// PaperMetadataPage is the paginated result of a bibliographic query. Each
// item identifies its source document through documentId.
type PaperMetadataPage struct {
	Items    []PaperMetadata `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	Total    int64           `json:"total"`
}

// PaperMetadataUpdate contains only editable bibliographic fields. Nil values
// leave persisted data unchanged, while an empty slice explicitly clears tags
// or authors.
type PaperMetadataUpdate struct {
	Title       *string        `json:"title,omitempty"`
	Abstract    *string        `json:"abstract,omitempty"`
	Authors     *[]PaperAuthor `json:"authors,omitempty"`
	Tags        *[]string      `json:"tags,omitempty"`
	SourceName  *string        `json:"sourceName,omitempty"`
	SourceURL   *string        `json:"sourceUrl,omitempty"`
	Venue       *string        `json:"venue,omitempty"`
	PublishedAt *time.Time     `json:"publishedAt,omitempty"`
	Volume      *string        `json:"volume,omitempty"`
	Issue       *string        `json:"issue,omitempty"`
	Pages       *string        `json:"pages,omitempty"`
	Language    *string        `json:"language,omitempty"`
	// MetadataSource is internal write provenance. HTTP clients cannot set it;
	// PaperService assigns manual while automated pipelines assign their source.
	MetadataSource *PaperMetadataSource `json:"-"`
}

// HasChanges reports whether this patch changes at least one paper field.
func (u *PaperMetadataUpdate) HasChanges() bool {
	return u != nil && (u.Title != nil || u.Abstract != nil || u.Authors != nil || u.Tags != nil ||
		u.SourceName != nil || u.SourceURL != nil || u.Venue != nil || u.PublishedAt != nil ||
		u.Volume != nil || u.Issue != nil || u.Pages != nil || u.Language != nil ||
		u.MetadataSource != nil)
}
