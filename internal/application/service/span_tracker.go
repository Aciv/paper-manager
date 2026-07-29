package service

import (
	"context"
	"fmt"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// SpanTracker records user-safe lifecycle state for background document work.
// It deliberately stores bounded error codes/reasons rather than raw provider
// responses or parser output.
type SpanTracker interface {
	Begin(ctx context.Context, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope, scopeID string, retryCount int) (uuid.UUID, error)
	Succeed(ctx context.Context, spanID uuid.UUID) error
	Fail(ctx context.Context, spanID uuid.UUID, code, reason string) error
	CancelDocument(ctx context.Context, documentID uuid.UUID, code, reason string) error
	GetView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error)
	HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error)
	FinalizeAttempt(ctx context.Context, documentID string, attempt int, status string, cause error, code, reason string)
}

type spanTracker struct {
	repo interfaces.DocumentProcessingSpanRepository
}

func NewSpanTracker(repo interfaces.DocumentProcessingSpanRepository) SpanTracker {
	return &spanTracker{repo: repo}
}

func (s *spanTracker) Begin(ctx context.Context, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind, scope, scopeID string, retryCount int) (uuid.UUID, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return uuid.Nil, nil
	}
	row, err := s.repo.UpsertRunning(ctx, types.DocumentProcessingSpan{DocumentID: documentID, Attempt: attempt, Kind: kind, Scope: scope, ScopeID: scopeID, RetryCount: retryCount})
	if err != nil {
		return uuid.Nil, err
	}
	logger.Info(ctx, "processing span started",
		"document_id", documentID,
		"attempt", row.Attempt,
		"kind", kind,
		"scope", row.Scope,
		"scope_id", row.ScopeID,
	)
	return row.ID, nil
}

func (s *spanTracker) Succeed(ctx context.Context, spanID uuid.UUID) error {
	if s == nil || s.repo == nil || spanID == uuid.Nil {
		return nil
	}
	return s.repo.Finish(ctx, spanID, types.ProcessingSpanSucceeded, "", "")
}

func (s *spanTracker) Fail(ctx context.Context, spanID uuid.UUID, code, reason string) error {
	if s == nil || s.repo == nil || spanID == uuid.Nil {
		return nil
	}
	return s.repo.Finish(ctx, spanID, types.ProcessingSpanFailed, code, safeSpanReason(reason))
}

func (s *spanTracker) CancelDocument(ctx context.Context, documentID uuid.UUID, code, reason string) error {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return nil
	}
	return s.repo.CancelActive(ctx, documentID, code, safeSpanReason(reason))
}

func (s *spanTracker) GetView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error) {
	view := types.DocumentProcessingView{DocumentID: document.ID, DocumentStatus: document.Status, Stages: []types.DocumentProcessingSpan{}}
	if s == nil || s.repo == nil {
		return view, nil
	}
	rows, err := s.repo.ListLatestAttempt(ctx, document.ID)
	if err != nil {
		return view, err
	}
	view.Stages = rows
	for _, row := range rows {
		if row.Attempt > view.LatestAttempt {
			view.LatestAttempt = row.Attempt
		}
	}
	return view, nil
}

func (s *spanTracker) HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return false, nil
	}
	return s.repo.HasActive(ctx, documentID, kinds...)
}

// FinalizeAttempt keeps dead-letter callbacks compatible with the original
// minimal tracker contract while closing every active stage in that attempt.
func (s *spanTracker) FinalizeAttempt(ctx context.Context, documentID string, attempt int, status string, cause error, code, reason string) {
	if s == nil || s.repo == nil || attempt <= 0 {
		return
	}
	id, err := uuid.Parse(documentID)
	if err != nil {
		return
	}
	terminal := types.ProcessingSpanFailed
	if status == string(types.ProcessingSpanCancelled) {
		terminal = types.ProcessingSpanCancelled
	}
	if err := s.repo.FinishAttempt(ctx, id, attempt, terminal, code, safeSpanReason(reason)); err != nil {
		logger.Warn(ctx, "finalize processing span attempt failed", "document_id", documentID, "attempt", attempt, "error", err)
	}
}

func safeSpanReason(reason string) string {
	if len(reason) > 512 {
		return reason[:512]
	}
	return reason
}

func spanFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	return "TASK_FAILED", safeSpanReason(fmt.Sprint(err))
}

var _ SpanTracker = (*spanTracker)(nil)
