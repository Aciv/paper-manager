package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const bibliographyTaskIDPrefix = "bibliography-"

func NewBibliographyGenerateTask(
	payload types.BibliographyTaskPayload, timeout time.Duration, maxRetry int, retention time.Duration,
) (*asynq.Task, []asynq.Option, error) {
	if err := payload.Validate(); err != nil {
		return nil, nil, err
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	if maxRetry < 0 {
		return nil, nil, errors.New("bibliography task max retry must not be negative")
	}
	if retention <= 0 {
		retention = 30 * time.Minute
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode bibliography task: %w", err)
	}
	taskID := bibliographyTaskIDPrefix + payload.RunID
	return asynq.NewTask(types.TypeBibliographyGenerate, encoded), []asynq.Option{
		asynq.Queue(types.QueueLow),
		asynq.Timeout(timeout),
		asynq.MaxRetry(maxRetry),
		asynq.Retention(retention),
		asynq.TaskID(taskID),
	}, nil
}

func (s *bibliographyService) EnqueueBaseBibliography(
	ctx context.Context, rawBaseID, requestID string,
) (types.BibliographyTask, error) {
	baseID, err := uuid.Parse(strings.TrimSpace(rawBaseID))
	if err != nil {
		return types.BibliographyTask{}, fmt.Errorf("invalid document base id: %w", err)
	}
	if _, err := s.repository.GetBibliographySnapshot(ctx, baseID, s.maxEntries); err != nil {
		return types.BibliographyTask{}, err
	}
	if s.tasks == nil {
		return types.BibliographyTask{}, types.ErrBibliographyTaskUnavailable
	}
	runID := uuid.New()
	payload := types.BibliographyTaskPayload{
		RequestID: strings.TrimSpace(requestID), DocumentBaseID: baseID.String(),
		RunID: runID.String(), Attempt: int(time.Now().UnixNano()),
	}
	task, options, err := NewBibliographyGenerateTask(payload, s.taskTimeout, s.taskMaxRetry, s.resultRetention)
	if err != nil {
		return types.BibliographyTask{}, err
	}
	info, err := s.tasks.EnqueueContext(ctx, task, options...)
	if err != nil {
		logger.Error(ctx, "enqueue bibliography generation failed", "document_base_id", baseID, "error", err)
		return types.BibliographyTask{}, fmt.Errorf("%w: enqueue bibliography generation: %v", types.ErrBibliographyTaskUnavailable, err)
	}
	logger.Info(ctx, "bibliography generation queued", "document_base_id", baseID, "task_id", info.ID)
	return types.BibliographyTask{ID: info.ID, BaseID: baseID, State: types.BibliographyTaskQueued}, nil
}

func (s *bibliographyService) GetBaseBibliographyTask(
	ctx context.Context, rawBaseID, taskID string,
) (types.BibliographyTask, error) {
	baseID, err := uuid.Parse(strings.TrimSpace(rawBaseID))
	if err != nil {
		return types.BibliographyTask{}, fmt.Errorf("invalid document base id: %w", err)
	}
	if !validBibliographyTaskID(taskID) {
		return types.BibliographyTask{}, types.ErrBibliographyTaskNotFound
	}
	if s.taskInspector == nil {
		return types.BibliographyTask{}, types.ErrBibliographyTaskUnavailable
	}
	record, err := s.taskInspector.GetTask(ctx, types.QueueLow, taskID)
	if err != nil {
		if errors.Is(err, types.ErrTaskNotFound) {
			return types.BibliographyTask{}, types.ErrBibliographyTaskNotFound
		}
		if errors.Is(err, types.ErrTaskInspectorUnavailable) {
			return types.BibliographyTask{}, types.ErrBibliographyTaskUnavailable
		}
		return types.BibliographyTask{}, err
	}
	if record.Type != types.TypeBibliographyGenerate {
		return types.BibliographyTask{}, types.ErrBibliographyTaskNotFound
	}
	var payload types.BibliographyTaskPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil || payload.Validate() != nil || payload.DocumentBaseID != baseID.String() {
		return types.BibliographyTask{}, types.ErrBibliographyTaskNotFound
	}
	result := types.BibliographyTask{ID: record.ID, BaseID: baseID}
	switch record.State {
	case "active":
		result.State = types.BibliographyTaskRunning
	case "completed":
		var snapshot types.BibliographySnapshot
		if err := json.Unmarshal(record.Result, &snapshot); err != nil {
			return types.BibliographyTask{}, fmt.Errorf("decode bibliography task result: %w", err)
		}
		result.State, result.Snapshot = types.BibliographyTaskSucceeded, &snapshot
	case "archived":
		result.State, result.ErrorCode = types.BibliographyTaskFailed, types.ProcessingErrorTaskFailed
	default:
		result.State = types.BibliographyTaskQueued
	}
	return result, nil
}

func validBibliographyTaskID(taskID string) bool {
	value := strings.TrimPrefix(strings.TrimSpace(taskID), bibliographyTaskIDPrefix)
	if value == taskID {
		return false
	}
	_, err := uuid.Parse(value)
	return err == nil
}

func (s *bibliographyService) ProcessBaseBibliography(ctx context.Context, task *asynq.Task) (outputErr error) {
	if task == nil {
		return fmt.Errorf("%w: bibliography task is required", asynq.SkipRetry)
	}
	var payload types.BibliographyTaskPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("%w: decode bibliography task: %v", asynq.SkipRetry, err)
	}
	if err := payload.Validate(); err != nil {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	writer := task.ResultWriter()
	if writer == nil {
		return errors.New("bibliography task result writer is unavailable")
	}
	return s.processBaseBibliographyPayload(ctx, payload, writer)
}

func (s *bibliographyService) processBaseBibliographyPayload(
	ctx context.Context, payload types.BibliographyTaskPayload, writer io.Writer,
) (outputErr error) {
	baseID, _ := uuid.Parse(payload.DocumentBaseID)
	ctx = context.WithValue(ctx, types.RequestIDContextKey, payload.RequestID)
	ctx = logger.WithRequestID(ctx, payload.RequestID)
	ctx = logger.WithAttrs(ctx, "document_base_id", baseID, "bibliography_run_id", payload.RunID)
	source, err := s.repository.GetBibliographySnapshot(ctx, baseID, s.maxEntries)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, types.ErrBibliographyBaseTooLarge) {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		return err
	}
	spanIDs := s.beginBibliographySpans(ctx, source, payload)
	completed := false
	defer func() {
		if completed {
			return
		}
		if !finalAsynqAttempt(ctx) && !errors.Is(outputErr, asynq.SkipRetry) && !errors.Is(outputErr, context.Canceled) {
			return
		}
		s.finishBibliographySpans(ctx, spanIDs, nil, outputErr)
	}()

	snapshot, err := s.generateBibliographySnapshot(ctx, source)
	if err != nil {
		if errors.Is(err, types.ErrBibliographyOutputTooLarge) {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
		}
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode bibliography task result: %w", err)
	}
	if int64(len(encoded)) > s.maxResponseBytes {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, types.ErrBibliographyOutputTooLarge)
	}
	if writer == nil {
		return errors.New("bibliography task result writer is unavailable")
	}
	if _, err := writer.Write(encoded); err != nil {
		return fmt.Errorf("write bibliography task result: %w", err)
	}
	completed = true
	s.finishBibliographySpans(ctx, spanIDs, snapshot.Entries, nil)
	logger.Info(ctx, "bibliography generation task completed", "entry_count", len(snapshot.Entries), "output_bytes", len(snapshot.BibTeX))
	return nil
}

func (s *bibliographyService) beginBibliographySpans(
	ctx context.Context, source types.BibliographySnapshotSource, payload types.BibliographyTaskPayload,
) map[uuid.UUID]uuid.UUID {
	spans := make(map[uuid.UUID]uuid.UUID, len(source.Entries))
	if s.spans == nil {
		return spans
	}
	retryCount, _ := asynq.GetRetryCount(ctx)
	scopeID := types.ComposeProcessingSpanScopeID(
		types.ProcessingSpanBibliographyExport, payload.DocumentBaseID, payload.RunID,
	)
	for _, entry := range source.Entries {
		spanID, err := s.spans.BeginSpan(ctx, entry.DocumentID, payload.Attempt,
			types.ProcessingSpanBibliographyExport, types.ProcessingSpanScopeBibliographyExport,
			scopeID, retryCount)
		if err != nil {
			logger.Warn(ctx, "start bibliography span failed", "document_id", entry.DocumentID, "error", err)
			continue
		}
		spans[entry.DocumentID] = spanID
	}
	return spans
}

func (s *bibliographyService) finishBibliographySpans(
	ctx context.Context, spanIDs map[uuid.UUID]uuid.UUID,
	reports []types.BibliographyEntryReport, taskErr error,
) {
	if s.spans == nil || len(spanIDs) == 0 {
		return
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageFinishTimeout)
	defer cancel()
	if taskErr != nil {
		status := types.ProcessingSpanFailed
		if errors.Is(taskErr, context.Canceled) {
			status = types.ProcessingSpanCancelled
		}
		code, reason := spanFailure(taskErr)
		for _, spanID := range spanIDs {
			_ = s.spans.FinishSpan(finishCtx, spanID, status, code, reason)
		}
		return
	}
	for _, report := range reports {
		spanID := spanIDs[report.DocumentID]
		if report.Outcome == types.BibliographyEntryGenerated {
			_ = s.spans.FinishSpan(finishCtx, spanID, types.ProcessingSpanSucceeded, "", "")
			continue
		}
		code := types.ProcessingErrorInvalidInput
		if report.Outcome == types.BibliographyEntrySkipped {
			code = types.ProcessingErrorNotFound
		}
		_ = s.spans.FinishSpan(finishCtx, spanID, types.ProcessingSpanFailed, code, "bibliography entry was not generated")
	}
}
