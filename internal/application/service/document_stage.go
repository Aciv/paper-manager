package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

func startDocumentStageTask(
	ctx context.Context,
	documents interfaces.DocumentRepository,
	documentID uuid.UUID,
	stage types.DocumentStage,
	generation int64,
) (bool, error) {
	if !types.HasDocumentStageEnvelope(stage, generation) {
		return false, nil
	}
	state, applied, err := documents.TryStartStage(ctx, documentID, stage, generation)
	if err != nil {
		return true, fmt.Errorf("start %s stage: %w", stage, err)
	}
	if !applied {
		return true, fmt.Errorf("%w: %s stage generation %d is %s", asynq.RevokeTask, stage, generation, state.Status)
	}
	return true, nil
}

func ensureDocumentStageWork(
	ctx context.Context,
	documents interfaces.DocumentRepository,
	documentID uuid.UUID,
	stage types.DocumentStage,
	generation int64,
) error {
	if !types.HasDocumentStageEnvelope(stage, generation) {
		return nil
	}
	allowed, err := documents.AllowsStageWork(ctx, documentID, stage, generation)
	if err != nil {
		return fmt.Errorf("check %s stage generation: %w", stage, err)
	}
	if !allowed {
		return fmt.Errorf("%w: %s stage generation %d is no longer running", asynq.RevokeTask, stage, generation)
	}
	return nil
}

func finishDocumentStageTask(
	ctx context.Context,
	documents interfaces.DocumentRepository,
	documentID uuid.UUID,
	stage types.DocumentStage,
	generation int64,
	taskErr error,
) {
	if !types.HasDocumentStageEnvelope(stage, generation) {
		return
	}
	finishCtx := context.WithoutCancel(ctx)
	if taskErr == nil {
		if _, applied, err := documents.FinishStage(finishCtx, documentID, stage, generation, types.DocumentStageSucceeded, "", ""); err != nil {
			if !ignoreDeletedDocumentStage(ctx, documentID, stage, err) {
				logger.Warn(ctx, "finish document stage failed", "document_id", documentID, "stage", stage, "generation", generation, "error", err)
			}
		} else if !applied {
			logger.Info(ctx, "document stage finish ignored after state change", "document_id", documentID, "stage", stage, "generation", generation)
		}
		return
	}
	if errors.Is(taskErr, asynq.RevokeTask) {
		return
	}
	if errors.Is(taskErr, asynq.SkipRetry) || documentStageErrorTerminal(taskErr) || finalAsynqAttempt(ctx) {
		code, message := spanFailure(taskErr)
		if _, _, err := documents.FinishStage(finishCtx, documentID, stage, generation, types.DocumentStageFailed, code, message); err != nil {
			if !ignoreDeletedDocumentStage(ctx, documentID, stage, err) {
				logger.Warn(ctx, "fail document stage failed", "document_id", documentID, "stage", stage, "generation", generation, "error", err)
			}
		}
		return
	}
	if _, _, err := documents.MarkStageRetryQueued(finishCtx, documentID, stage, generation); err != nil {
		if !ignoreDeletedDocumentStage(ctx, documentID, stage, err) {
			logger.Warn(ctx, "queue document stage retry failed", "document_id", documentID, "stage", stage, "generation", generation, "error", err)
		}
	}
}

// ignoreDeletedDocumentStage treats a missing document row as a normal
// no-op. A newest-wins duplicate replacement can delete the document whose
// source-lookup task is finishing.
func ignoreDeletedDocumentStage(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, err error,
) bool {
	if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	logger.Info(ctx, "document stage finish skipped after document removal",
		"document_id", documentID, "stage", stage)
	return true
}

func documentStageErrorTerminal(err error) bool {
	return errors.Is(err, types.ErrInvalidRecognitionRequest) ||
		errors.Is(err, types.ErrInvalidPaperInfoTaskPayload) ||
		errors.Is(err, types.ErrInvalidPaperExtraction) ||
		errors.Is(err, types.ErrInvalidPaperCatalogData) ||
		errors.Is(err, types.ErrInvalidRemotePDFRequest) ||
		errors.Is(err, types.ErrUnsupportedRecognitionContentType) ||
		errors.Is(err, ErrPaperInfoCatalogUnavailable) ||
		errors.Is(err, ErrInvalidPaperMetadata) ||
		errors.Is(err, ErrPaperCatalogNoMatch)
}

func stageAttempt(generation int64, legacyAttempt int) int {
	if generation < 1 {
		return positiveAttempt(legacyAttempt)
	}
	if generation > int64(math.MaxInt) {
		return math.MaxInt
	}
	return int(generation)
}

// enqueueDocumentStageTask treats an existing deterministic task ID as a
// successful handoff. Stage task IDs include document, stage, and generation,
// so a conflict can only identify the same durable stage attempt.
func enqueueDocumentStageTask(
	ctx context.Context,
	enqueuer interfaces.TaskEnqueuer,
	task *asynq.Task,
	options []asynq.Option,
	documentID string,
) error {
	err := enqueueManagedTask(ctx, enqueuer, task, options, documentID)
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		logger.Debug(ctx, "document stage task already exists",
			"document_id", documentID,
			"task_type", task.Type())
		return nil
	}
	return err
}
