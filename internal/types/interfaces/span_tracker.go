package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// SpanRecorder records the user-safe lifecycle of asynchronous document work.
// It is intentionally independent of document source state: parsing, metadata
// extraction, source lookup, and reference lookup can each progress or fail
// without adding an aggregate processing-status column to documents.
//
// Implementations persist only bounded error codes and safe reasons. They must
// not store raw parser output, provider responses, credentials, or uploads.
type SpanRecorder interface {
	// BeginSpan creates or refreshes the queued/running span identified by the
	// document, attempt, kind, scope, and scope ID tuple. Asynq retries reuse
	// the same tuple so one logical operation remains one timeline entry.
	BeginSpan(ctx context.Context, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, retryCount int) (uuid.UUID, error)
	// BeginChildSpan is BeginSpan with an explicit parent for a fine-grained
	// operation that runs within the same stage execution, such as one catalog
	// provider request. It does not model cross-queue workflow ownership.
	BeginChildSpan(ctx context.Context, parentSpanID uuid.UUID, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, retryCount int) (uuid.UUID, error)
	// Heartbeat records that an active operation remains alive without changing
	// its status. Long parser and provider calls use it to keep parent timelines
	// fresh while their external I/O is in progress.
	Heartbeat(ctx context.Context, spanID uuid.UUID) error
	// FinishSpan terminally closes one span. Implementations canonicalize the
	// error code/reason before persistence and never revive a terminal row.
	FinishSpan(ctx context.Context, spanID uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error
	// CancelStageProjection marks matching active observation rows cancelled
	// after a business stage fence has been committed. It never controls worker
	// permission and callers must treat its failure as telemetry-only.
	CancelStageProjection(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64, code, reason string) error
	// AggregateParentDisplay closes a display parent once all direct children
	// are terminal. It never changes document-stage control state.
	AggregateParentDisplay(ctx context.Context, childSpanID uuid.UUID) (types.ProcessingSpanStatus, error)
}

// ProcessingViewQuery is the read-only processing projection exposed to HTTP
// handlers. It is deliberately separate from the writer interface so workers
// cannot use span observation as a control plane.
type ProcessingViewQuery interface {
	GetView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error)
}

// SpanTracker is the single implementation object backing both narrow views.
// Dependency injection gives workers SpanRecorder and handlers
// ProcessingViewQuery, never this broad interface.
type SpanTracker interface {
	SpanRecorder
	ProcessingViewQuery
}

// DocumentProcessingSpanRepository persists document pipeline state without
// coupling services to GORM.
type DocumentProcessingSpanRepository interface {
	// UpsertRunning creates a queued/running stage row or refreshes its existing
	// unique attempt/kind/scope tuple. Queue retries call this method so they
	// continue one durable timeline entry rather than creating duplicate spans.
	UpsertRunning(ctx context.Context, span types.DocumentProcessingSpan) (types.DocumentProcessingSpan, error)
	// Heartbeat refreshes updated_at only while a span remains active.
	Heartbeat(ctx context.Context, id uuid.UUID) error
	// Finish changes one span identified by id to a terminal status and stores a
	// short, user-safe error code and reason when it did not succeed.
	Finish(ctx context.Context, id uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error
	// ListCurrent returns at most limit latest rows for durable stage tuples
	// belonging to a document.
	ListCurrent(ctx context.Context, documentID uuid.UUID, limit int) ([]types.DocumentProcessingSpan, error)
	// ListTimeline is the separate bounded history primitive. The default HTTP
	// view does not call it, avoiding an accidental full-history response.
	ListTimeline(ctx context.Context, documentID uuid.UUID, limit int) ([]types.DocumentProcessingSpan, error)
	// LatestAttempt performs a bounded aggregate independent of stage response
	// limits so retry numbering never depends on which rows fit in the UI view.
	LatestAttempt(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind) (int, error)
	// CancelActiveStage marks active observation rows associated with an exact
	// business document-stage generation. It is projection only.
	CancelActiveStage(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64, code, reason string) error
	// AggregateParentDisplay aggregates direct child terminal states solely for
	// display. Document-stage state owns workflow completion independently.
	AggregateParentDisplay(ctx context.Context, childSpanID uuid.UUID) (types.ProcessingSpanStatus, error)
}
