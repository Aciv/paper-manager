package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// SpanTracker records the user-safe lifecycle of asynchronous document work.
// It is intentionally independent of DocumentStatus: parsing, metadata
// extraction, source lookup, and reference lookup can each progress or fail
// without adding a persistent status column to documents.
//
// Implementations persist only bounded error codes and safe reasons. They must
// not store raw parser output, provider responses, credentials, or uploads.
type SpanTracker interface {
	// Begin creates or refreshes the queued/running span identified by the
	// document, attempt, kind, scope, and scope ID tuple. Asynq retries reuse
	// the same tuple so one logical operation remains one timeline entry.
	Begin(ctx context.Context, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, retryCount int) (uuid.UUID, error)
	// BeginChild is Begin with an explicit durable parent. It preserves one
	// processing tree across document boundaries for shared references.
	BeginChild(ctx context.Context, parentSpanID uuid.UUID, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, retryCount int) (uuid.UUID, error)
	// Heartbeat records that an active operation remains alive without changing
	// its status. Long parser and provider calls use it to keep parent timelines
	// fresh while their external I/O is in progress.
	Heartbeat(ctx context.Context, spanID uuid.UUID) error
	// Succeed marks one span as successfully completed.
	Succeed(ctx context.Context, spanID uuid.UUID) error
	// Fail marks one span as failed with a short user-safe code and reason.
	Fail(ctx context.Context, spanID uuid.UUID, code, reason string) error
	// CancelDocument closes all currently active spans for one document. It is
	// used by document parsing cancellation and does not rewrite terminal rows.
	CancelDocument(ctx context.Context, documentID uuid.UUID, code, reason string) error
	// CancelReferenceRuns closes only active bibliography runs and their child
	// batch/provider spans. It returns the run IDs whose matching queue tasks
	// must be removed or signalled by the caller.
	CancelReferenceRuns(ctx context.Context, documentID uuid.UUID, code, reason string) ([]uuid.UUID, error)
	// CancelReferenceDownloadRuns closes active bibliography acquisition roots
	// and all of their child URL/download/deduplication spans.
	CancelReferenceDownloadRuns(ctx context.Context, documentID uuid.UUID, code, reason string) ([]uuid.UUID, error)
	// ScopeCancelled reports whether one exact operation was cancelled before a
	// queued worker began. It prevents an upsert from reactivating the marker.
	ScopeCancelled(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error)
	// FinalizeParent closes a parent span once all direct children are terminal,
	// propagating a child failure while preserving an explicit cancellation.
	FinalizeParent(ctx context.Context, childSpanID uuid.UUID) error
	// ReferenceRunActive reports whether a bibliography run has not been
	// cancelled. Workers call it before external I/O and again before writes to
	// prevent late results from a cancelled run reaching the database.
	ReferenceRunActive(ctx context.Context, documentID, runID uuid.UUID) (bool, error)
	// GetView returns the document's latest-attempt, API-safe processing
	// timeline, including the current lifecycle status and ordered span rows.
	GetView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error)
	// HasActive reports whether the document has active spans of any requested
	// kinds. With no kinds it checks every processing stage.
	HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error)
	// FinalizeAttempt terminally closes all active spans in an attempt after a
	// dead-letter event. The original cause is accepted for compatibility but
	// implementations persist only the supplied safe code and reason.
	FinalizeAttempt(ctx context.Context, documentID string, attempt int, status string, cause error, code, reason string)
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
	// FinishAttempt terminally updates every active span belonging to one
	// document attempt. It is used after cancellation or dead-letter exhaustion
	// so descendant stages cannot remain displayed as running.
	FinishAttempt(ctx context.Context, documentID uuid.UUID, attempt int, status types.ProcessingSpanStatus, code, reason string) error
	// ListLatestAttempt returns the latest row for every durable stage tuple of
	// a document. Recognition attempts and paper-info attempts are independent,
	// so it must not discard a completed parse merely because a later metadata
	// retry used a newer attempt number.
	ListLatestAttempt(ctx context.Context, documentID uuid.UUID) ([]types.DocumentProcessingSpan, error)
	// HasActive reports whether the document has queued or running spans of any
	// requested kind. An empty kinds list checks all kinds and supports retry
	// guards without requiring callers to inspect every stage row.
	HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error)
	// CancelActive marks every currently active span for the document as
	// cancelled, recording the supplied safe code and reason. Terminal rows are
	// preserved unchanged for timeline history.
	CancelActive(ctx context.Context, documentID uuid.UUID, code, reason string) error
	// HasActiveScope reports whether an active span exists for one exact
	// kind/scope/scope-id tuple. Reference workers use the run-root row as a
	// durable cancellation checkpoint before making a provider call or writing
	// a late result.
	HasActiveScope(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error)
	// ListActiveScopeIDs returns active scope IDs for the requested stage and
	// scope. It lets a control request cancel only bibliography runs rather than
	// unrelated document, extraction, or source-catalog work.
	ListActiveScopeIDs(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope) ([]types.ProcessingSpanScopeID, error)
	// CancelActiveScope marks only active rows in one exact scope tuple as
	// cancelled. It is deliberately narrower than CancelActive.
	CancelActiveScope(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, code, reason string) error
	// CancelActiveScopePrefix terminally closes active child spans whose
	// scope_id begins with prefix. It is used to cancel one reference run's
	// provider/batch children without touching other document operations.
	CancelActiveScopePrefix(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, prefix types.ProcessingSpanScopeID, code, reason string) error
	// IsScopeCancelled reports whether a durable span marker was explicitly
	// cancelled, including a terminal run-root changed by a user cancellation.
	IsScopeCancelled(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error)
	// ForceCancelScope records cancellation even when a run-root has already
	// completed its enqueue phase. It provides a durable late-result guard.
	ForceCancelScope(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, code, reason string) error
	// CancelDescendants closes every active child below parentSpanID. It is used
	// for bibliography acquisition cancellation where targets are documents
	// different from the source paper that owns the run root.
	CancelDescendants(ctx context.Context, parentSpanID uuid.UUID, code, reason string) error
	// FinalizeParentFromChildren aggregates direct child terminal states for the
	// parent of childSpanID and no-ops while any sibling remains active.
	FinalizeParentFromChildren(ctx context.Context, childSpanID uuid.UUID) error
}
