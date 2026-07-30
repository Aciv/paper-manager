package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

const processingSpanHeartbeatInterval = 15 * time.Second

type spanTracker struct {
	repo interfaces.DocumentProcessingSpanRepository
}

// NewSpanTracker creates the durable application implementation of
// interfaces.SpanTracker.
func NewSpanTracker(repo interfaces.DocumentProcessingSpanRepository) interfaces.SpanTracker {
	return &spanTracker{repo: repo}
}

func (s *spanTracker) Begin(ctx context.Context, documentID uuid.UUID,
	attempt int, kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int) (uuid.UUID, error) {
	return s.begin(ctx, uuid.Nil, documentID, attempt, kind, scope, scopeID, retryCount)
}

func (s *spanTracker) BeginChild(ctx context.Context, parentSpanID uuid.UUID,
	documentID uuid.UUID, attempt int,
	kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int) (uuid.UUID, error) {
	return s.begin(ctx, parentSpanID, documentID, attempt, kind, scope, scopeID, retryCount)
}

func (s *spanTracker) begin(ctx context.Context,
	parentSpanID uuid.UUID,
	documentID uuid.UUID,
	attempt int,
	kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int) (uuid.UUID, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return uuid.Nil, nil
	}
	span := types.DocumentProcessingSpan{DocumentID: documentID,
		Attempt:    attempt,
		Kind:       kind,
		Scope:      scope,
		ScopeID:    scopeID,
		RetryCount: retryCount}
	if parentSpanID != uuid.Nil {
		span.ParentSpanID = &parentSpanID
	}
	row, err := s.repo.UpsertRunning(ctx, span)
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

func (s *spanTracker) Heartbeat(ctx context.Context, spanID uuid.UUID) error {
	if s == nil || s.repo == nil || spanID == uuid.Nil {
		return nil
	}
	return s.repo.Heartbeat(ctx, spanID)
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

func (s *spanTracker) CancelReferenceRuns(ctx context.Context, documentID uuid.UUID, code, reason string) ([]uuid.UUID, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return nil, nil
	}
	values, err := s.repo.ListActiveScopeIDs(
		ctx,
		documentID,
		types.ProcessingSpanCatalogReference,
		types.ProcessingSpanScopeReferenceBatch)
	if err != nil {
		return nil, err
	}
	runs := make([]uuid.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		parts := types.SplitProcessingSpanScopeID(value)
		if len(parts) < 3 || parts[0] != string(types.ProcessingSpanCatalogReference) {
			continue
		}
		runID, parseErr := uuid.Parse(parts[1])
		if parseErr != nil || runID == uuid.Nil {
			continue
		}
		if _, duplicate := seen[runID]; duplicate {
			continue
		}
		seen[runID] = struct{}{}
		if err := s.repo.ForceCancelScope(
			ctx,
			documentID,
			types.ProcessingSpanCatalogReference,
			types.ProcessingSpanScopeReferenceRun,
			types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogReference, runID.String()),
			code,
			safeSpanReason(reason)); err != nil {
			return runs, err
		}
		batchPrefix := types.ProcessingSpanScopeID(
			types.ComposeProcessingSpanScopeID(
				types.ProcessingSpanCatalogReference,
				runID.String(),
			).String() + ":")
		if err := s.repo.CancelActiveScopePrefix(
			ctx,
			documentID,
			types.ProcessingSpanCatalogReference,
			types.ProcessingSpanScopeReferenceBatch,
			batchPrefix,
			code,
			safeSpanReason(reason)); err != nil {
			return runs, err
		}
		providerPrefix := types.ProcessingSpanScopeID(
			types.ComposeProcessingSpanScopeID(
				types.ProcessingSpanCatalogProvider,
				string(types.ProcessingSpanCatalogReference),
				runID.String(),
			).String() + ":")
		if err := s.repo.CancelActiveScopePrefix(
			ctx,
			documentID,
			types.ProcessingSpanCatalogProvider,
			types.ProcessingSpanScopeProvider,
			providerPrefix,
			code,
			safeSpanReason(reason)); err != nil {
			return runs, err
		}
		runs = append(runs, runID)
	}
	return runs, nil
}

func (s *spanTracker) ReferenceRunActive(ctx context.Context, documentID, runID uuid.UUID) (bool, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil || runID == uuid.Nil {
		return false, nil
	}
	cancelled, err := s.repo.IsScopeCancelled(
		ctx,
		documentID,
		types.ProcessingSpanCatalogReference,
		types.ProcessingSpanScopeReferenceRun,
		types.ComposeProcessingSpanScopeID(
			types.ProcessingSpanCatalogReference,
			runID.String()))
	return !cancelled, err
}

func (s *spanTracker) GetView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error) {
	view := types.DocumentProcessingView{DocumentID: document.ID,
		DocumentStatus: document.Status,
		Stages:         []types.DocumentProcessingSpan{}}
	if s == nil || s.repo == nil {
		return view, nil
	}
	rows, err := s.repo.ListLatestAttempt(ctx, document.ID)
	if err != nil {
		return view, err
	}
	view.Stages = rows
	for _, row := range rows {
		// The top-level attempt label belongs to source recognition. Paper-info
		// retries use their own attempt sequence and must not make a completed
		// parse disappear or misleadingly relabel it in the UI.
		if row.Kind == types.ProcessingSpanRecognition && row.Attempt > view.LatestAttempt {
			view.LatestAttempt = row.Attempt
		}
	}
	return view, nil
}

func (s *spanTracker) HasActive(ctx context.Context,
	documentID uuid.UUID,
	kinds ...types.ProcessingSpanKind) (bool, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return false, nil
	}
	return s.repo.HasActive(ctx, documentID, kinds...)
}

// FinalizeAttempt keeps dead-letter callbacks compatible with the original
// minimal tracker contract while closing every active stage in that attempt.
func (s *spanTracker) FinalizeAttempt(ctx context.Context,
	documentID string,
	attempt int,
	status string,
	cause error,
	code,
	reason string) {
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

// keepSpanAlive refreshes an active span while a bounded external operation is
// running. It stops with the operation context, never revives a terminal row,
// and intentionally treats telemetry failure as non-fatal.
func keepSpanAlive(ctx context.Context, tracker interfaces.SpanTracker, spanID uuid.UUID) func() {
	if tracker == nil || spanID == uuid.Nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(processingSpanHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if err := tracker.Heartbeat(context.WithoutCancel(ctx), spanID); err != nil {
					logger.Warn(ctx, "processing span heartbeat failed", "span_id", spanID, "error", err)
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func spanFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	return "TASK_FAILED", safeSpanReason(fmt.Sprint(err))
}

var _ interfaces.SpanTracker = (*spanTracker)(nil)
