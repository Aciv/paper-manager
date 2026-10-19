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

type spanTracker struct {
	repo interfaces.DocumentProcessingSpanRepository
}

// NewSpanTracker creates the single span implementation. Its recorder and
// view-query interfaces are deliberately injected separately.
func NewSpanTracker(repo interfaces.DocumentProcessingSpanRepository) interfaces.SpanTracker {
	return &spanTracker{repo: repo}
}

func NewSpanRecorder(tracker interfaces.SpanTracker) interfaces.SpanRecorder { return tracker }

func NewProcessingViewQuery(tracker interfaces.SpanTracker) interfaces.ProcessingViewQuery {
	return tracker
}

func (s *spanTracker) BeginSpan(ctx context.Context, documentID uuid.UUID,
	attempt int, kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int) (uuid.UUID, error) {
	return s.begin(ctx, uuid.Nil, documentID, attempt, kind, scope, scopeID, retryCount)
}

func (s *spanTracker) BeginChildSpan(ctx context.Context, parentSpanID uuid.UUID,
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

func (s *spanTracker) Heartbeat(ctx context.Context, spanID uuid.UUID) error {
	if s == nil || s.repo == nil || spanID == uuid.Nil {
		return nil
	}
	return s.repo.Heartbeat(ctx, spanID)
}

func (s *spanTracker) FinishSpan(ctx context.Context, spanID uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error {
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

func (s *spanTracker) CancelStageProjection(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64, code, reason string) error {
	if s == nil || s.repo == nil || documentID == uuid.Nil || generation < 1 {
		return nil
	}
	code, reason = safeSpanOutcome(code, reason)
	return s.repo.CancelActiveStage(ctx, documentID, stage, generation, code, reason)
}

func (s *spanTracker) AggregateParentDisplay(ctx context.Context, childSpanID uuid.UUID) (types.ProcessingSpanStatus, error) {
	if s == nil || s.repo == nil || childSpanID == uuid.Nil {
		return "", nil
	}
	return s.repo.AggregateParentDisplay(ctx, childSpanID)
}

func (s *spanTracker) GetView(ctx context.Context, document types.Document) (types.DocumentProcessingView, error) {
	view := newDocumentProcessingView(document)
	if s == nil || s.repo == nil {
		return view, nil
	}
	rows, err := s.repo.ListCurrent(ctx, document.ID, 100)
	if err != nil {
		return view, err
	}
	view.Stages = sanitizeProcessingSpans(rows)
	return view, nil
}

func (s *spanTracker) GetViews(
	ctx context.Context, documents []types.Document,
) ([]types.DocumentProcessingView, error) {
	views := make([]types.DocumentProcessingView, len(documents))
	for index, document := range documents {
		views[index] = newDocumentProcessingView(document)
	}
	if s == nil || s.repo == nil || len(documents) == 0 {
		return views, nil
	}
	ids := make([]uuid.UUID, 0, len(documents))
	for _, document := range documents {
		ids = append(ids, document.ID)
	}
	rowsByDocument, err := s.repo.ListCurrentBatch(ctx, ids, 100)
	if err != nil {
		return nil, err
	}
	for index := range views {
		views[index].Stages = sanitizeProcessingSpans(rowsByDocument[views[index].DocumentID])
	}
	return views, nil
}

func newDocumentProcessingView(document types.Document) types.DocumentProcessingView {
	return types.DocumentProcessingView{
		DocumentID: document.ID, SourceState: document.SourceState,
		CanonicalDocumentID: document.CanonicalDocumentID,
		StageStates:         documentStageStates(document), Stages: []types.DocumentProcessingSpan{},
	}
}

func sanitizeProcessingSpans(rows []types.DocumentProcessingSpan) []types.DocumentProcessingSpan {
	if rows == nil {
		return []types.DocumentProcessingSpan{}
	}
	for i := range rows {
		rows[i].ErrorCode, rows[i].ErrorReason = safeSpanOutcome(rows[i].ErrorCode, rows[i].ErrorReason)
		rows[i].Attributes = types.JSON(`{}`)
	}
	return rows
}

func documentStageStates(document types.Document) []types.DocumentStageState {
	stages := []types.DocumentStage{types.DocumentStageMarkdown, types.DocumentStageMetadataExtraction, types.DocumentStageSourceLookup, types.DocumentStageReferenceLookup}
	states := make([]types.DocumentStageState, 0, len(stages))
	for _, stage := range stages {
		state, err := document.StageState(stage)
		if err != nil {
			continue
		}
		if state.ErrorCode != nil {
			code, message := safeSpanOutcome(*state.ErrorCode, stringValue(state.ErrorMessage))
			state.ErrorCode, state.ErrorMessage = &code, &message
		}
		states = append(states, state)
	}
	return states
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

var _ interfaces.SpanTracker = (*spanTracker)(nil)
