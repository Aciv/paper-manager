package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stageFinishTimeout bounds bookkeeping writes that deliberately detach from
// the cancelled task context. A stuck finish must not hold a database
// connection open until the pool is exhausted.
const stageFinishTimeout = 5 * time.Second

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
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageFinishTimeout)
	defer cancel()
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
	// The router uses the same classifier (including the final-attempt
	// decision below) to choose Revoke/Skip/retry. A SkipRetry or terminal
	// error must therefore leave the stage failed, never queued.
	switch ClassifyTaskError(taskErr) {
	case TaskDispositionRevoke:
		return
	case TaskDispositionSkip:
		code, message := spanFailure(taskErr)
		if _, _, err := documents.FinishStage(finishCtx, documentID, stage, generation, types.DocumentStageFailed, code, message); err != nil {
			if !ignoreDeletedDocumentStage(ctx, documentID, stage, err) {
				logger.Warn(ctx, "fail document stage failed", "document_id", documentID, "stage", stage, "generation", generation, "error", err)
			}
		}
		return
	}
	if finalAsynqAttempt(ctx) {
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

// TaskDisposition is the single classification shared by the Asynq router and
// the durable stage-finish path. Keeping both sides on one function prevents a
// task from being archived as SkipRetry while its stage row is left queued
// with no consumer.
type TaskDisposition int

const (
	// TaskDispositionRetry lets Asynq schedule another attempt.
	TaskDispositionRetry TaskDisposition = iota
	// TaskDispositionSkip stops retrying because the input or service state
	// cannot improve on another attempt. The stage must end failed.
	TaskDispositionSkip
	// TaskDispositionRevoke drops work that lost its generation fence or was
	// cancelled. No stage transition is needed.
	TaskDispositionRevoke
)

// ClassifyTaskError maps one handler error onto Asynq's three dispositions.
// Revoke wins over skip because stale/cancelled work must not be retried or
// recorded as a failure. Explicit sentinels returned by handlers are honoured
// before the type-based fallbacks.
func ClassifyTaskError(err error) TaskDisposition {
	if err == nil {
		return TaskDispositionRetry
	}
	if errors.Is(err, asynq.RevokeTask) {
		return TaskDispositionRevoke
	}
	if errors.Is(err, asynq.SkipRetry) {
		return TaskDispositionSkip
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, ErrStalePaperInfo) {
		return TaskDispositionRevoke
	}
	if terminalTaskError(err) {
		return TaskDispositionSkip
	}
	if retryableExternal, classifiedExternal := externalTaskRetryability(err); classifiedExternal && !retryableExternal {
		return TaskDispositionSkip
	}
	return TaskDispositionRetry
}

// terminalTaskError lists deterministic validation failures. Retrying these
// can never succeed without a new user action or fresh input.
func terminalTaskError(err error) bool {
	return errors.Is(err, types.ErrInvalidRecognitionRequest) ||
		errors.Is(err, types.ErrInvalidPaperInfoTaskPayload) ||
		errors.Is(err, types.ErrInvalidPaperExtraction) ||
		errors.Is(err, types.ErrInvalidPaperCatalogData) ||
		errors.Is(err, types.ErrInvalidPaperCitation) ||
		errors.Is(err, types.ErrInvalidRemotePDFRequest) ||
		errors.Is(err, types.ErrUnsupportedRecognitionContentType) ||
		errors.Is(err, ErrPaperInfoCatalogUnavailable) ||
		errors.Is(err, ErrInvalidPaperMetadata) ||
		errors.Is(err, ErrPaperCatalogNoMatch)
}

// externalTaskRetryability inspects provider/gRPC errors. Transient transport
// failures retry; input and permission failures skip. Unclassified errors
// (including database errors) keep the retry default.
func externalTaskRetryability(err error) (retryable bool, classified bool) {
	WalkTaskErrors(err, func(item error) {
		if retryClass, ok := item.(interface{ TaskRetryable() bool }); ok {
			classified = true
			retryable = retryable || retryClass.TaskRetryable()
		}
		switch status.Code(item) {
		case codes.Canceled:
			classified = true
		case codes.InvalidArgument,
			codes.NotFound,
			codes.AlreadyExists,
			codes.PermissionDenied,
			codes.Unauthenticated,
			codes.FailedPrecondition,
			codes.OutOfRange,
			codes.Unimplemented:
			classified = true
		case codes.DeadlineExceeded,
			codes.ResourceExhausted,
			codes.Aborted,
			codes.Internal,
			codes.Unavailable,
			codes.DataLoss:
			classified = true
			retryable = true
		}
	})
	return retryable, classified
}

// WalkTaskErrors visits err and every error it wraps, including joined errors.
// The router reuses it for provider Retry-After extraction.
func WalkTaskErrors(err error, visit func(error)) {
	if err == nil {
		return
	}
	visit(err)
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, nested := range wrapped.Unwrap() {
			WalkTaskErrors(nested, visit)
		}
	case interface{ Unwrap() error }:
		WalkTaskErrors(wrapped.Unwrap(), visit)
	}
}
