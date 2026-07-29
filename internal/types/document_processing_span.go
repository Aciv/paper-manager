package types

import (
	"time"

	"github.com/google/uuid"
)

// ProcessingSpanStatus is the durable, user-safe state of one asynchronous
// document operation. It is intentionally separate from DocumentStatus.
type ProcessingSpanStatus string

const (
	ProcessingSpanQueued    ProcessingSpanStatus = "queued"
	ProcessingSpanRunning   ProcessingSpanStatus = "running"
	ProcessingSpanSucceeded ProcessingSpanStatus = "succeeded"
	ProcessingSpanFailed    ProcessingSpanStatus = "failed"
	ProcessingSpanCancelled ProcessingSpanStatus = "cancelled"
)

// Active reports whether a span is still awaiting execution or currently
// executing. Callers use it to decide whether the UI should poll and whether
// a retry action must be rejected in favour of cancellation.
func (s ProcessingSpanStatus) Active() bool {
	return s == ProcessingSpanQueued || s == ProcessingSpanRunning
}

// ProcessingSpanKind identifies the independently observable pipeline stage.
type ProcessingSpanKind string

const (
	ProcessingSpanRecognition      ProcessingSpanKind = "recognition"
	ProcessingSpanMetadataExtract  ProcessingSpanKind = "metadata_extraction"
	ProcessingSpanCatalogSource    ProcessingSpanKind = "catalog_source"
	ProcessingSpanCatalogReference ProcessingSpanKind = "catalog_reference"
	ProcessingSpanCatalogProvider  ProcessingSpanKind = "catalog_provider"
)

// DocumentProcessingSpan is one idempotent stage in a document attempt.
// The unique tuple makes queue retries update the same durable timeline row.
type DocumentProcessingSpan struct {
	ID          uuid.UUID            `gorm:"type:uuid;primaryKey" json:"id"`
	DocumentID  uuid.UUID            `gorm:"column:document_id;type:uuid;not null;index" json:"documentId"`
	Attempt     int                  `gorm:"not null;default:1" json:"attempt"`
	Kind        ProcessingSpanKind   `gorm:"type:text;not null" json:"kind"`
	Scope       string               `gorm:"type:text;not null;default:'document'" json:"scope"`
	ScopeID     string               `gorm:"type:text;not null;default:''" json:"scopeId,omitempty"`
	Status      ProcessingSpanStatus `gorm:"type:text;not null" json:"status"`
	RetryCount  int                  `gorm:"not null;default:0" json:"retryCount"`
	ErrorCode   string               `gorm:"type:text;not null;default:''" json:"errorCode,omitempty"`
	ErrorReason string               `gorm:"type:text;not null;default:''" json:"errorReason,omitempty"`
	Attributes  JSON                 `gorm:"type:jsonb;not null;default:'{}'" json:"attributes,omitempty"`
	StartedAt   time.Time            `gorm:"not null" json:"startedAt"`
	FinishedAt  *time.Time           `json:"finishedAt,omitempty"`
	CreatedAt   time.Time            `json:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt"`
}

// TableName pins the GORM model to the document_processing_spans table instead
// of relying on GORM's inferred pluralisation. This keeps the model aligned
// with the versioned SQL migration.
func (DocumentProcessingSpan) TableName() string { return "document_processing_spans" }

// DocumentProcessingView is the compact API representation used by the UI.
type DocumentProcessingView struct {
	DocumentID     uuid.UUID                `json:"documentId"`
	DocumentStatus DocumentStatus           `json:"documentStatus"`
	LatestAttempt  int                      `json:"latestAttempt"`
	Stages         []DocumentProcessingSpan `json:"stages"`
}
