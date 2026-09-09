package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// SpanRecorder records the user-safe lifecycle of asynchronous document work.
// It is intentionally independent of DocumentStatus: parsing, metadata
// extraction, source lookup, and reference lookup can each progress or fail
// without adding a persistent status column to documents.
//
// Implementations persist only bounded error codes and safe reasons. They must
// not store raw parser output, provider responses, credentials, or uploads.
type SpanRecorder interface {
	// BeginSpan creates or refreshes the queued/running span identified by the
	// document, attempt, kind, scope, and scope ID tuple. Asynq retries reuse
	// the same tuple so one logical operation remains one timeline entry.
	BeginSpan(ctx context.Context, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, retryCount int) (uuid.UUID, error)
	// BeginChildSpan is BeginSpan with an explicit durable parent. It preserves one
	// processing tree across document boundaries for shared references.
	BeginChildSpan(ctx context.Context, parentSpanID uuid.UUID, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, retryCount int) (uuid.UUID, error)
	// Heartbeat records that an active operation remains alive without changing
	// its status. Long parser and provider calls use it to keep parent timelines
	// fresh while their external I/O is in progress.
	Heartbeat(ctx context.Context, spanID uuid.UUID) error
	// FinishSpan terminally closes one span. Implementations canonicalize the
	// error code/reason before persistence and never revive a terminal row.
	FinishSpan(ctx context.Context, spanID uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error
	// CancelSpansForRun projects a durable run cancellation onto all active
	// observation rows linked to that run. It has no workflow-specific scope or
	// queue knowledge.
	CancelSpansForRun(ctx context.Context, runID uuid.UUID, code, reason string) error
	// ScopeCancelled reports whether one exact operation was cancelled before a
	// queued worker began. It prevents an upsert from reactivating the marker.
	ScopeCancelled(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error)
	// AggregateParentDisplay closes a display parent once all direct children
	// are terminal. It never changes ProcessingRun control state.
	AggregateParentDisplay(ctx context.Context, childSpanID uuid.UUID) (types.ProcessingSpanStatus, error)
	// GetRunView returns a bounded run summary and latest-stage API projection.
	// timeline, including the current lifecycle status and ordered span rows.
	GetRunView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error)
	// HasActive reports whether the document has active spans of any requested
	// kinds. With no kinds it checks every processing stage.
	HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error)
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
	// belonging to a document or one of its selected runs.
	ListCurrent(ctx context.Context, documentID uuid.UUID, runIDs []uuid.UUID, limit int) ([]types.DocumentProcessingSpan, error)
	// ListTimeline is the separate bounded history primitive. The default HTTP
	// view does not call it, avoiding an accidental full-history response.
	ListTimeline(ctx context.Context, documentID uuid.UUID, limit int) ([]types.DocumentProcessingSpan, error)
	// LatestAttempt performs a bounded aggregate independent of stage response
	// limits so retry numbering never depends on which rows fit in the UI view.
	LatestAttempt(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind) (int, error)
	// HasActive reports whether the document has queued or running spans of any
	// requested kind. An empty kinds list checks all kinds and supports retry
	// guards without requiring callers to inspect every stage row.
	HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error)
	// CancelActiveRun marks every active span linked to runID cancelled across
	// document boundaries. It is a pure observation projection.
	CancelActiveRun(ctx context.Context, runID uuid.UUID, code, reason string) error
	// IsScopeCancelled reports whether a durable span marker was explicitly
	// cancelled, including a terminal run-root changed by a user cancellation.
	IsScopeCancelled(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error)
	// AggregateParentDisplay aggregates direct child terminal states solely for
	// display. ProcessingRun owns workflow completion independently.
	AggregateParentDisplay(ctx context.Context, childSpanID uuid.UUID) (types.ProcessingSpanStatus, error)
}
