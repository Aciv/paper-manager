package types

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrDuplicateDOI is returned when a non-empty DOI already belongs to a document.
var (
	ErrDuplicateDOI            = errors.New("document with this DOI already exists")
	ErrDuplicateFileHash       = errors.New("document with this file hash already exists")
	ErrDocumentNotProcessing   = errors.New("document is no longer processing")
	ErrUnknownDocumentConflict = errors.New(
		"unknown placeholder ID belongs to a source document",
	)
)

// DocumentSourceState describes only whether a durable source object exists.
// Workflow progress and failures live in the four independent stage fields.
type DocumentSourceState string

const (
	DocumentSourceMissing   DocumentSourceState = "missing"
	DocumentSourceAvailable DocumentSourceState = "available"
	DocumentSourceDeleting  DocumentSourceState = "deleting"
	DocumentSourceMerged    DocumentSourceState = "merged"
)

func (state DocumentSourceState) Valid() bool {
	switch state {
	case DocumentSourceMissing, DocumentSourceAvailable, DocumentSourceDeleting, DocumentSourceMerged:
		return true
	default:
		return false
	}
}

// DocumentStatus is the lifecycle state persisted in documents.status.
type DocumentStatus string

const (
	// DocumentStatusUnknown identifies a bibliographic placeholder without a
	// source file. It must not be queued for document recognition.
	DocumentStatusUnknown    DocumentStatus = "unknown"
	DocumentStatusUploaded   DocumentStatus = "uploaded"
	DocumentStatusQueued     DocumentStatus = "queued"
	DocumentStatusProcessing DocumentStatus = "processing"
	DocumentStatusCancelled  DocumentStatus = "cancelled"
	DocumentStatusDeleting   DocumentStatus = "deleting"
	DocumentStatusReady      DocumentStatus = "ready"
	DocumentStatusFailed     DocumentStatus = "failed"
)

const (
	// UnknownDocumentContentType distinguishes bibliographic placeholders from
	// uploaded source files.
	UnknownDocumentContentType = "application/x-paper-reference"
	// UnknownDocumentStorageScheme provides a collision-free internal key for a
	// placeholder that deliberately has no object in FileStorage.
	UnknownDocumentStorageScheme = "unknown://"
)

// Document maps to the documents table.
type Document struct {
	ID                             uuid.UUID           `gorm:"column:id;type:uuid;primaryKey" json:"id"`
	OriginalFilename               string              `gorm:"column:original_filename;type:text;not null" json:"originalFilename"`
	ContentType                    string              `gorm:"column:content_type;type:text;not null" json:"contentType"`
	SizeBytes                      int64               `gorm:"column:size_bytes;not null;check:size_bytes >= 0" json:"sizeBytes"`
	DOI                            string              `gorm:"column:doi;type:text;not null;default:'';index:idx_documents_doi" json:"doi,omitempty"`
	FileSHA256                     string              `gorm:"column:file_sha256;type:char(64);not null;default:''" json:"-"`
	StorageKey                     string              `gorm:"column:storage_key;type:text;not null;uniqueIndex" json:"-"`
	Status                         DocumentStatus      `gorm:"column:status;type:text;not null;default:uploaded;check:chk_documents_status,status IN ('unknown','uploaded','queued','processing','cancelled','deleting','ready','failed')" json:"status"`
	ErrorMessage                   *string             `gorm:"column:error_message;type:text" json:"errorMessage,omitempty"`
	SourceState                    DocumentSourceState `gorm:"column:source_state;type:text;not null;default:available" json:"sourceState"`
	CanonicalDocumentID            *uuid.UUID          `gorm:"column:canonical_document_id;type:uuid" json:"canonicalDocumentId,omitempty"`
	MarkdownStatus                 DocumentStageStatus `gorm:"column:markdown_status;type:text;not null;default:not_started" json:"markdownStatus"`
	MarkdownGeneration             int64               `gorm:"column:markdown_generation;not null;default:0" json:"markdownGeneration"`
	MarkdownUpdatedAt              time.Time           `gorm:"column:markdown_updated_at;not null;default:CURRENT_TIMESTAMP" json:"markdownUpdatedAt"`
	MarkdownErrorCode              *string             `gorm:"column:markdown_error_code;type:text" json:"markdownErrorCode,omitempty"`
	MarkdownErrorMessage           *string             `gorm:"column:markdown_error_message;type:text" json:"markdownErrorMessage,omitempty"`
	MetadataExtractionStatus       DocumentStageStatus `gorm:"column:metadata_extraction_status;type:text;not null;default:not_started" json:"metadataExtractionStatus"`
	MetadataExtractionGeneration   int64               `gorm:"column:metadata_extraction_generation;not null;default:0" json:"metadataExtractionGeneration"`
	MetadataExtractionUpdatedAt    time.Time           `gorm:"column:metadata_extraction_updated_at;not null;default:CURRENT_TIMESTAMP" json:"metadataExtractionUpdatedAt"`
	MetadataExtractionErrorCode    *string             `gorm:"column:metadata_extraction_error_code;type:text" json:"metadataExtractionErrorCode,omitempty"`
	MetadataExtractionErrorMessage *string             `gorm:"column:metadata_extraction_error_message;type:text" json:"metadataExtractionErrorMessage,omitempty"`
	SourceLookupStatus             DocumentStageStatus `gorm:"column:source_lookup_status;type:text;not null;default:not_started" json:"sourceLookupStatus"`
	SourceLookupGeneration         int64               `gorm:"column:source_lookup_generation;not null;default:0" json:"sourceLookupGeneration"`
	SourceLookupUpdatedAt          time.Time           `gorm:"column:source_lookup_updated_at;not null;default:CURRENT_TIMESTAMP" json:"sourceLookupUpdatedAt"`
	SourceLookupErrorCode          *string             `gorm:"column:source_lookup_error_code;type:text" json:"sourceLookupErrorCode,omitempty"`
	SourceLookupErrorMessage       *string             `gorm:"column:source_lookup_error_message;type:text" json:"sourceLookupErrorMessage,omitempty"`
	ReferenceLookupStatus          DocumentStageStatus `gorm:"column:reference_lookup_status;type:text;not null;default:not_started" json:"referenceLookupStatus"`
	ReferenceLookupGeneration      int64               `gorm:"column:reference_lookup_generation;not null;default:0" json:"referenceLookupGeneration"`
	ReferenceLookupUpdatedAt       time.Time           `gorm:"column:reference_lookup_updated_at;not null;default:CURRENT_TIMESTAMP" json:"referenceLookupUpdatedAt"`
	ReferenceLookupErrorCode       *string             `gorm:"column:reference_lookup_error_code;type:text" json:"referenceLookupErrorCode,omitempty"`
	ReferenceLookupErrorMessage    *string             `gorm:"column:reference_lookup_error_message;type:text" json:"referenceLookupErrorMessage,omitempty"`
	CreatedAt                      time.Time           `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt                      time.Time           `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (Document) TableName() string { return "documents" }

const DocumentPageSize = 10
const MaxDocumentSearchPage = 10000

// DocumentSearch describes source-document lookup. Bibliographic title and
// tag filtering belongs to PaperSearch and PaperMetadataRepository.
// A zero-value Status means "all statuses".
type DocumentSearch struct {
	ID          *uuid.UUID
	DOI         string
	FileSHA256  string
	Status      DocumentStatus
	SourceState DocumentSourceState
	// ExcludeUnknown omits bibliography placeholders that do not have a
	// locally stored source file. It is primarily used by reading views.
	ExcludeUnknown bool
	Page           int
	PageSize       int
}

// DocumentPage is the paginated result returned by document search endpoints.
type DocumentPage struct {
	Items    []Document `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
	Total    int64      `json:"total"`
}
