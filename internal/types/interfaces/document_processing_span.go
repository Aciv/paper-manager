package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// DocumentProcessingSpanRepository persists document pipeline state without
// coupling services to GORM.
type DocumentProcessingSpanRepository interface {
	// UpsertRunning creates a queued/running stage row or refreshes its existing
	// unique attempt/kind/scope tuple. Queue retries call this method so they
	// continue one durable timeline entry rather than creating duplicate spans.
	UpsertRunning(ctx context.Context, span types.DocumentProcessingSpan) (types.DocumentProcessingSpan, error)
	// Finish changes one span identified by id to a terminal status and stores a
	// short, user-safe error code and reason when it did not succeed.
	Finish(ctx context.Context, id uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error
	// FinishAttempt terminally updates every active span belonging to one
	// document attempt. It is used after cancellation or dead-letter exhaustion
	// so descendant stages cannot remain displayed as running.
	FinishAttempt(ctx context.Context, documentID uuid.UUID, attempt int, status types.ProcessingSpanStatus, code, reason string) error
	// ListLatestAttempt returns all stage rows for the document's most recent
	// attempt, ordered for construction of the document processing API view.
	ListLatestAttempt(ctx context.Context, documentID uuid.UUID) ([]types.DocumentProcessingSpan, error)
	// HasActive reports whether the document has queued or running spans of any
	// requested kind. An empty kinds list checks all kinds and supports retry
	// guards without requiring callers to inspect every stage row.
	HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error)
	// CancelActive marks every currently active span for the document as
	// cancelled, recording the supplied safe code and reason. Terminal rows are
	// preserved unchanged for timeline history.
	CancelActive(ctx context.Context, documentID uuid.UUID, code, reason string) error
}
