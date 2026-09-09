package types

import (
	"strings"
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

// Processing span error codes form the public persistence allowlist. Unknown
// internal errors are collapsed to ProcessingErrorTaskFailed before storage or
// API serialization.
const (
	ProcessingErrorTaskFailed         = "TASK_FAILED"
	ProcessingErrorCancelled          = "CANCELLED"
	ProcessingErrorUserCancelled      = "USER_CANCELLED"
	ProcessingErrorTimeout            = "TIMEOUT"
	ProcessingErrorNotFound           = "NOT_FOUND"
	ProcessingErrorUnsupportedContent = "UNSUPPORTED_CONTENT"
	ProcessingErrorServiceUnavailable = "SERVICE_UNAVAILABLE"
	ProcessingErrorInvalidInput       = "INVALID_INPUT"
	ProcessingErrorNoMatch            = "NO_MATCH"
	ProcessingErrorQueue              = "QUEUE_ERROR"
	ProcessingErrorTaskBuild          = "TASK_BUILD_ERROR"
	ProcessingErrorSpanStart          = "SPAN_START_FAILED"
	ProcessingErrorDocumentProcess    = "DOCUMENT_PROCESS_FAILED"
	ProcessingErrorReferenceDispatch  = "REFERENCE_DISPATCH_FAILED"
	ProcessingErrorDownloadDispatch   = "REFERENCE_DOWNLOAD_DISPATCH_FAILED"
	ProcessingErrorChildFailed        = "CHILD_FAILED"
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
	// ProcessingSpanPipeline is the persistent root of one DocumentService
	// controlled workflow through source-catalog handoff. The bibliography
	// dispatch branch owns an independent reference-run span after handoff.
	ProcessingSpanPipeline               ProcessingSpanKind = "pipeline"
	ProcessingSpanRecognition            ProcessingSpanKind = "recognition"
	ProcessingSpanMetadataExtract        ProcessingSpanKind = "metadata_extraction"
	ProcessingSpanCatalogSource          ProcessingSpanKind = "catalog_source"
	ProcessingSpanCatalogReference       ProcessingSpanKind = "catalog_reference"
	ProcessingSpanCatalogProvider        ProcessingSpanKind = "catalog_provider"
	ProcessingSpanReferenceDownload      ProcessingSpanKind = "reference_download"
	ProcessingSpanResolvePDFURL          ProcessingSpanKind = "resolve_pdf_url"
	ProcessingSpanDownloadPDF            ProcessingSpanKind = "download_pdf"
	ProcessingSpanDeduplicateDocument    ProcessingSpanKind = "deduplicate_document"
	ProcessingSpanEnqueueDocumentProcess ProcessingSpanKind = "enqueue_document_process"
)

// ProcessingSpanScope identifies the ownership level of a processing span.
// It is distinct from PaperCatalogScope: the latter selects task behaviour,
// while this type makes timeline rows cancellable and aggregatable.
type ProcessingSpanScope string

const (
	// ProcessingSpanScopeProcess owns one persistent process root.
	ProcessingSpanScopeProcess ProcessingSpanScope = "process"
	// ProcessingSpanScopeDocument owns one document-level pipeline stage.
	ProcessingSpanScopeDocument ProcessingSpanScope = "document"
	// ProcessingSpanScopeCatalogSource owns source-paper catalog resolution.
	ProcessingSpanScopeCatalogSource ProcessingSpanScope = "source"
	// ProcessingSpanScopeCatalogReference owns a legacy single-reference task.
	ProcessingSpanScopeCatalogReference ProcessingSpanScope = "reference"
	// ProcessingSpanScopeReferenceRun owns one user-cancellable bibliography run.
	ProcessingSpanScopeReferenceRun ProcessingSpanScope = "reference_run"
	// ProcessingSpanScopeReferenceBatch owns one provider-assigned reference batch.
	ProcessingSpanScopeReferenceBatch ProcessingSpanScope = "reference_batch"
	// ProcessingSpanScopeProvider owns one external catalog-provider request.
	ProcessingSpanScopeProvider             ProcessingSpanScope = "provider"
	ProcessingSpanScopeReferenceDownloadRun ProcessingSpanScope = "reference_download_run"
	ProcessingSpanScopeRemoteDocument       ProcessingSpanScope = "remote_document"
)

// ProcessingSpanScopeID is the durable, encoded identifier within a span
// scope. Every value follows one format: "<kind>:<part1>:<part2>:...".
type ProcessingSpanScopeID string

func (id ProcessingSpanScopeID) String() string { return string(id) }

// PaperCatalogProvider identifies a configured paper-catalog adapter. Unknown
// values remain valid to permit future providers; NormalizePaperCatalogProvider
// still gives every caller one canonical representation.
type PaperCatalogProvider string

const (
	PaperCatalogProviderArXiv           PaperCatalogProvider = "arxiv"
	PaperCatalogProviderSemanticScholar PaperCatalogProvider = "semantic_scholar"
	PaperCatalogProviderOpenAlex        PaperCatalogProvider = "openalex"
	PaperCatalogProviderCrossref        PaperCatalogProvider = "crossref"
)

func (provider PaperCatalogProvider) String() string { return string(provider) }

// NormalizePaperCatalogProvider trims and lowercases a provider identity for
// task payloads, span keys, throttles, and registry lookup.
func NormalizePaperCatalogProvider(value string) PaperCatalogProvider {
	return PaperCatalogProvider(strings.ToLower(strings.TrimSpace(value)))
}

// ComposeProcessingSpanScopeID builds the one canonical scope-ID form. The
// caller supplies the predefined span kind plus stable identifiers such as a
// document UUID, a reference-run UUID, or a normalized provider name. Empty
// parts are omitted; scope-ID parts must not themselves contain ':'.
func ComposeProcessingSpanScopeID(kind ProcessingSpanKind, parts ...string) ProcessingSpanScopeID {
	values := make([]string, 0, len(parts)+1)
	if value := strings.TrimSpace(string(kind)); value != "" {
		values = append(values, value)
	}
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return ProcessingSpanScopeID(strings.Join(values, ":"))
}

// SplitProcessingSpanScopeID returns the kind and identifier parts from the
// canonical scope-ID form. It is used only where cancellation must inspect a
// durable ID; producers should always use ComposeProcessingSpanScopeID.
func SplitProcessingSpanScopeID(value ProcessingSpanScopeID) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value.String(), ":")
}

// DocumentProcessingSpan is one idempotent stage in a document attempt.
// The unique tuple makes queue retries update the same durable timeline row.
type DocumentProcessingSpan struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	DocumentID uuid.UUID `gorm:"column:document_id;type:uuid;not null;index" json:"documentId"`
	// RunID is nullable while legacy queued tasks and pre-migration rows remain.
	// New producers attach spans to the durable control-plane run in Phase 2.
	RunID *uuid.UUID `gorm:"column:run_id;type:uuid;index" json:"runId,omitempty"`
	// ParentSpanID links a fine-grained operation (for example, one provider
	// request) to the durable stage that scheduled it. A child may belong to a
	// different document: reference placeholders are shared by several source
	// papers, while their run root belongs to the source document.
	ParentSpanID *uuid.UUID `gorm:"column:parent_span_id;type:uuid;index" json:"parentSpanId,omitempty"`
	// Go int is 64-bit on supported deployments; migration 19 widens the
	// PostgreSQL column to BIGINT so Unix-nanosecond manual attempts fit.
	Attempt     int                   `gorm:"not null;default:1" json:"attempt"`
	Kind        ProcessingSpanKind    `gorm:"type:text;not null" json:"kind"`
	Scope       ProcessingSpanScope   `gorm:"type:text;not null;default:'document'" json:"scope"`
	ScopeID     ProcessingSpanScopeID `gorm:"type:text;not null;default:''" json:"scopeId,omitempty"`
	Status      ProcessingSpanStatus  `gorm:"type:text;not null" json:"status"`
	RetryCount  int                   `gorm:"not null;default:0" json:"retryCount"`
	ErrorCode   string                `gorm:"type:text;not null;default:''" json:"errorCode,omitempty"`
	ErrorReason string                `gorm:"type:text;not null;default:''" json:"errorReason,omitempty"`
	Attributes  JSON                  `gorm:"type:jsonb;not null;default:'{}'" json:"attributes,omitempty"`
	StartedAt   time.Time             `gorm:"not null" json:"startedAt"`
	FinishedAt  *time.Time            `json:"finishedAt,omitempty"`
	CreatedAt   time.Time             `json:"createdAt"`
	UpdatedAt   time.Time             `json:"updatedAt"`
}

// TableName pins the GORM model to the document_processing_spans table instead
// of relying on GORM's inferred pluralisation. This keeps the model aligned
// with the versioned SQL migration.
func (DocumentProcessingSpan) TableName() string { return "document_processing_spans" }

// DocumentProcessingView is the compact API representation used by the UI.
type DocumentProcessingView struct {
	DocumentID        uuid.UUID                `json:"documentId"`
	DocumentStatus    DocumentStatus           `json:"documentStatus"`
	LatestAttempt     int                      `json:"latestAttempt"`
	Runs              []ProcessingRun          `json:"runs,omitempty"`
	Stages            []DocumentProcessingSpan `json:"stages"`
	ReferenceProgress *PaperReferenceProgress  `json:"referenceProgress,omitempty"`
}
