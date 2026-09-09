package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

const processingSpanHeartbeatInterval = 15 * time.Second

type spanRecorder struct {
	repo interfaces.DocumentProcessingSpanRepository
	runs interfaces.ProcessingRunRepository
}

// NewSpanRecorder creates the durable, observation-only span implementation.
func NewSpanRecorder(repo interfaces.DocumentProcessingSpanRepository, runs interfaces.ProcessingRunRepository) interfaces.SpanRecorder {
	return &spanRecorder{repo: repo, runs: runs}
}

func (s *spanRecorder) BeginSpan(ctx context.Context, documentID uuid.UUID,
	attempt int, kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int) (uuid.UUID, error) {
	return s.begin(ctx, uuid.Nil, documentID, attempt, kind, scope, scopeID, retryCount)
}

func (s *spanRecorder) BeginChildSpan(ctx context.Context, parentSpanID uuid.UUID,
	documentID uuid.UUID, attempt int,
	kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int) (uuid.UUID, error) {
	return s.begin(ctx, parentSpanID, documentID, attempt, kind, scope, scopeID, retryCount)
}

func (s *spanRecorder) begin(ctx context.Context,
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
	if runID := processingRunID(ctx); runID != uuid.Nil {
		span.RunID = &runID
	}
	if parentSpanID != uuid.Nil {
		span.ParentSpanID = &parentSpanID
	}
	row, err := s.repo.UpsertRunning(ctx, span)
	if err != nil {
		return uuid.Nil, err
	}
	if !row.Status.Active() {
		return row.ID, fmt.Errorf("processing span is already terminal: %s", row.Status)
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

func (s *spanRecorder) Heartbeat(ctx context.Context, spanID uuid.UUID) error {
	if s == nil || s.repo == nil || spanID == uuid.Nil {
		return nil
	}
	return s.repo.Heartbeat(ctx, spanID)
}

func (s *spanRecorder) FinishSpan(ctx context.Context, spanID uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error {
	if s == nil || s.repo == nil || spanID == uuid.Nil {
		return nil
	}
	switch status {
	case types.ProcessingSpanSucceeded:
		code, reason = "", ""
	case types.ProcessingSpanFailed, types.ProcessingSpanCancelled:
		code, reason = safeSpanOutcome(code, reason)
	default:
		return fmt.Errorf("processing span terminal status %q is invalid", status)
	}
	return s.repo.Finish(ctx, spanID, status, code, reason)
}

func (s *spanRecorder) CancelSpansForRun(ctx context.Context, runID uuid.UUID, code, reason string) error {
	if s == nil || s.repo == nil || runID == uuid.Nil {
		return nil
	}
	code, reason = safeSpanOutcome(code, reason)
	return s.repo.CancelActiveRun(ctx, runID, code, reason)
}

func (s *spanRecorder) ScopeCancelled(
	ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID,
) (bool, error) {
	if s == nil || s.repo == nil {
		return false, nil
	}
	return s.repo.IsScopeCancelled(ctx, documentID, kind, scope, scopeID)
}

func (s *spanRecorder) AggregateParentDisplay(ctx context.Context, childSpanID uuid.UUID) (types.ProcessingSpanStatus, error) {
	if s == nil || s.repo == nil || childSpanID == uuid.Nil {
		return "", nil
	}
	return s.repo.AggregateParentDisplay(ctx, childSpanID)
}

func (s *spanRecorder) GetRunView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error) {
	view := types.DocumentProcessingView{DocumentID: document.ID,
		DocumentStatus: document.Status,
		Stages:         []types.DocumentProcessingSpan{}}
	if s == nil || s.repo == nil {
		return view, nil
	}
	runIDs := []uuid.UUID{}
	if s.runs != nil {
		runs, err := s.runs.ListRecent(ctx, document.ID, 20)
		if err != nil {
			return view, err
		}
		for index := range runs {
			runs[index].ErrorCode, runs[index].ErrorReason = safeSpanOutcome(runs[index].ErrorCode, runs[index].ErrorReason)
			runIDs = append(runIDs, runs[index].ID)
		}
		view.Runs = runs
	}
	rows, err := s.repo.ListCurrent(ctx, document.ID, runIDs, 100)
	if err != nil {
		return view, err
	}
	for index := range rows {
		rows[index].ErrorCode, rows[index].ErrorReason = safeSpanOutcome(rows[index].ErrorCode, rows[index].ErrorReason)
		// Attributes are internal diagnostics. No attribute key is currently in
		// the public allowlist, so legacy/high-cardinality values stay server-side.
		rows[index].Attributes = types.JSON(`{}`)
	}
	view.Stages = rows
	view.LatestAttempt, err = s.repo.LatestAttempt(ctx, document.ID, types.ProcessingSpanRecognition)
	if err != nil {
		return view, err
	}
	return view, nil
}

func (s *spanRecorder) HasActive(ctx context.Context,
	documentID uuid.UUID,
	kinds ...types.ProcessingSpanKind) (bool, error) {
	if s == nil || s.repo == nil || documentID == uuid.Nil {
		return false, nil
	}
	return s.repo.HasActive(ctx, documentID, kinds...)
}

// keepSpanAlive refreshes an active span while a bounded external operation is
// running. It stops with the operation context, never revives a terminal row,
// and intentionally treats telemetry failure as non-fatal.
func keepSpanAlive(ctx context.Context, tracker interfaces.SpanRecorder, spanID uuid.UUID) func() {
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
	switch {
	case errors.Is(err, context.Canceled):
		return types.ProcessingErrorCancelled, "processing task was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return types.ProcessingErrorTimeout, "processing task timed out"
	case errors.Is(err, sql.ErrNoRows):
		return types.ProcessingErrorNotFound, "required processing data was not found"
	case errors.Is(err, types.ErrUnsupportedRecognitionContentType):
		return types.ProcessingErrorUnsupportedContent, "document content type is not supported"
	case errors.Is(err, types.ErrRecognitionServiceUnavailable),
		errors.Is(err, ErrPaperInfoExtractionUnavailable),
		errors.Is(err, ErrPaperInfoCatalogUnavailable),
		errors.Is(err, types.ErrRemotePDFUnavailable):
		return types.ProcessingErrorServiceUnavailable, "a required processing service is unavailable"
	case errors.Is(err, types.ErrInvalidRecognitionRequest),
		errors.Is(err, types.ErrInvalidPaperInfoTaskPayload),
		errors.Is(err, types.ErrInvalidPaperExtraction),
		errors.Is(err, types.ErrInvalidPaperCatalogData),
		errors.Is(err, types.ErrInvalidRemotePDFRequest),
		errors.Is(err, ErrInvalidPaperMetadata):
		return types.ProcessingErrorInvalidInput, "processing input is invalid"
	case errors.Is(err, ErrPaperCatalogNoMatch):
		return types.ProcessingErrorNoMatch, "no satisfactory catalog match was found"
	default:
		return types.ProcessingErrorTaskFailed, "processing task failed"
	}
}

func safeSpanOutcome(code, _ string) (string, string) {
	switch code {
	case "":
		return "", ""
	case types.ProcessingErrorCancelled, types.ProcessingErrorUserCancelled:
		return code, "processing task was cancelled"
	case types.ProcessingErrorTimeout:
		return code, "processing task timed out"
	case types.ProcessingErrorNotFound:
		return code, "required processing data was not found"
	case types.ProcessingErrorUnsupportedContent:
		return code, "document content type is not supported"
	case types.ProcessingErrorServiceUnavailable:
		return code, "a required processing service is unavailable"
	case types.ProcessingErrorInvalidInput:
		return code, "processing input is invalid"
	case types.ProcessingErrorNoMatch:
		return code, "no satisfactory catalog match was found"
	case types.ProcessingErrorQueue:
		return code, "processing task could not be queued"
	case types.ProcessingErrorTaskBuild:
		return code, "processing task could not be created"
	case types.ProcessingErrorSpanStart:
		return code, "processing observation could not start"
	case types.ProcessingErrorDocumentProcess, types.ProcessingErrorReferenceDispatch,
		types.ProcessingErrorDownloadDispatch, types.ProcessingErrorTaskFailed:
		return code, "processing task failed"
	case types.ProcessingErrorChildFailed:
		return code, "one or more workflow items failed"
	default:
		return types.ProcessingErrorTaskFailed, "processing task failed"
	}
}

var _ interfaces.SpanRecorder = (*spanRecorder)(nil)
