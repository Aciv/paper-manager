package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type processingRunContextKey struct{}

func withProcessingRun(ctx context.Context, runID uuid.UUID) context.Context {
	if runID == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, processingRunContextKey{}, runID)
}

func processingRunID(ctx context.Context) uuid.UUID {
	runID, _ := ctx.Value(processingRunContextKey{}).(uuid.UUID)
	return runID
}

func createQueuedProcessingRun(
	ctx context.Context,
	runs interfaces.ProcessingRunRepository,
	runID, ownerID uuid.UUID,
	kind types.ProcessingWorkflowKind,
	attempt int64,
	idempotencyKey string,
) (uuid.UUID, error) {
	if runs == nil {
		return runID, nil
	}
	if runID == uuid.Nil {
		runID = uuid.New()
	}
	if attempt < 1 {
		attempt = 1
	}
	stored, _, err := runs.CreateQueued(ctx, types.ProcessingRun{
		ID:              runID,
		OwnerDocumentID: ownerID,
		WorkflowKind:    kind,
		Attempt:         attempt,
		IdempotencyKey:  idempotencyKey,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return stored.ID, nil
}

func processingRunKey(kind types.ProcessingWorkflowKind, ownerID uuid.UUID, attempt int64, runID uuid.UUID) string {
	if runID != uuid.Nil {
		return string(kind) + ":" + ownerID.String() + ":" + runID.String()
	}
	return string(kind) + ":" + ownerID.String() + ":" + strconv.FormatInt(attempt, 10)
}

// startProcessingRun makes the durable CAS the first business operation in a
// new worker. A missing row is the rolling-upgrade legacy path: payloads
// emitted before processing_runs existed may still contain a span UUID.
func startProcessingRun(
	ctx context.Context,
	runs interfaces.ProcessingRunRepository,
	runID uuid.UUID,
) (context.Context, bool, error) {
	if runs == nil || runID == uuid.Nil {
		return ctx, false, nil
	}
	run, applied, err := runs.TryStart(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return ctx, false, nil
	}
	if err != nil {
		return ctx, false, fmt.Errorf("start processing run: %w", err)
	}
	if !applied {
		if run.State == types.ProcessingRunCancelRequested {
			_, _, finishErr := runs.Finish(context.WithoutCancel(ctx), runID, types.ProcessingRunCancelled,
				"CANCELLED", "processing run was cancelled before worker start")
			if finishErr != nil {
				logger.Warn(ctx, "finish cancelled processing run failed", "run_id", runID, "error", finishErr)
			}
		}
		return ctx, true, fmt.Errorf("%w: processing run is %s", asynq.RevokeTask, run.State)
	}
	return withProcessingRun(ctx, runID), true, nil
}

func processingRunAllowsWork(ctx context.Context, runs interfaces.ProcessingRunRepository, runID uuid.UUID) (bool, error) {
	if runs == nil || runID == uuid.Nil {
		return true, nil
	}
	run, err := runs.Get(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return run.State == types.ProcessingRunQueued || run.State == types.ProcessingRunRunning, nil
}

func finishProcessingRunTask(
	ctx context.Context,
	runs interfaces.ProcessingRunRepository,
	runID uuid.UUID,
	controlled bool,
	taskErr error,
	completeOnSuccess bool,
	failureCode, failureReason string,
) {
	if runs == nil || runID == uuid.Nil || !controlled {
		return
	}
	finishCtx := context.WithoutCancel(ctx)
	if taskErr == nil {
		if completeOnSuccess {
			run, getErr := runs.Get(finishCtx, runID)
			if getErr != nil {
				if !errors.Is(getErr, sql.ErrNoRows) {
					logger.Warn(ctx, "load processing run before finish failed", "run_id", runID, "error", getErr)
				}
				return
			}
			state, code, reason := types.ProcessingRunSucceeded, "", ""
			if run.State == types.ProcessingRunCancelRequested {
				state, code, reason = types.ProcessingRunCancelled, "CANCELLED", "processing run was cancelled"
			}
			if _, _, err := runs.Finish(finishCtx, runID, state, code, reason); err != nil {
				logger.Warn(ctx, "finish processing run failed", "run_id", runID, "error", err)
			}
		}
		return
	}
	if errors.Is(taskErr, asynq.RevokeTask) || errors.Is(taskErr, context.Canceled) {
		run, err := runs.Get(finishCtx, runID)
		if err == nil && run.State == types.ProcessingRunCancelRequested {
			_, _, err = runs.Finish(finishCtx, runID, types.ProcessingRunCancelled, "CANCELLED", "processing run was cancelled")
		} else if err == nil {
			_, _, err = runs.Finish(finishCtx, runID, types.ProcessingRunFailed, failureCode, failureReason)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			logger.Warn(ctx, "finish cancelled processing run failed", "run_id", runID, "error", err)
		}
		return
	}
	terminal := errors.Is(taskErr, asynq.SkipRetry) || processingRunErrorTerminal(taskErr) || finalAsynqAttempt(ctx)
	if terminal {
		if _, _, err := runs.Finish(finishCtx, runID, types.ProcessingRunFailed, failureCode, failureReason); err != nil {
			logger.Warn(ctx, "finish failed processing run failed", "run_id", runID, "error", err)
		}
		return
	}
	if _, _, err := runs.MarkRetryQueued(finishCtx, runID, failureCode, failureReason); err != nil {
		logger.Warn(ctx, "queue processing run retry failed", "run_id", runID, "error", err)
	}
}

func startProcessingRunItem(
	ctx context.Context, runs interfaces.ProcessingRunRepository, runID uuid.UUID, itemKey string,
) (bool, error) {
	if runs == nil || runID == uuid.Nil || itemKey == "" {
		return false, nil
	}
	item, applied, err := runs.TryStartItem(ctx, runID, itemKey)
	if errors.Is(err, sql.ErrNoRows) {
		// Rolling-upgrade tasks may belong to a run created before durable fan-out
		// items existed. The run fence still protects those tasks.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("start processing run item: %w", err)
	}
	if !applied {
		return true, fmt.Errorf("%w: processing run item is %s", asynq.RevokeTask, item.State)
	}
	return true, nil
}

func finishProcessingRunItemTask(
	ctx context.Context, runs interfaces.ProcessingRunRepository, runID uuid.UUID, itemKey string,
	controlled bool, taskErr error,
) {
	if runs == nil || runID == uuid.Nil || itemKey == "" || !controlled {
		return
	}
	finishCtx := context.WithoutCancel(ctx)
	if taskErr == nil {
		if _, err := runs.FinishItem(finishCtx, runID, itemKey, types.ProcessingRunSucceeded, "", ""); err != nil {
			logger.Warn(ctx, "finish processing run item failed", "run_id", runID, "item_key", itemKey, "error", err)
		}
		return
	}
	if errors.Is(taskErr, asynq.RevokeTask) || errors.Is(taskErr, context.Canceled) {
		run, err := runs.Get(finishCtx, runID)
		if err == nil && run.State == types.ProcessingRunCancelRequested {
			_, err = runs.FinishItem(finishCtx, runID, itemKey, types.ProcessingRunCancelled,
				types.ProcessingErrorCancelled, "workflow item was cancelled")
		} else if err == nil && errors.Is(taskErr, context.Canceled) {
			err = runs.MarkItemRetry(finishCtx, runID, itemKey)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			logger.Warn(ctx, "resolve interrupted processing run item failed", "run_id", runID, "item_key", itemKey, "error", err)
		}
		return
	}
	if errors.Is(taskErr, asynq.SkipRetry) || processingRunErrorTerminal(taskErr) || finalAsynqAttempt(ctx) {
		if _, err := runs.FinishItem(finishCtx, runID, itemKey, types.ProcessingRunFailed,
			"TASK_FAILED", "workflow item failed"); err != nil {
			logger.Warn(ctx, "fail processing run item failed", "run_id", runID, "item_key", itemKey, "error", err)
		}
		return
	}
	if err := runs.MarkItemRetry(finishCtx, runID, itemKey); err != nil {
		logger.Warn(ctx, "queue processing run item retry failed", "run_id", runID, "item_key", itemKey, "error", err)
	}
}

// processingRunErrorTerminal mirrors the application-level errors which the
// router classifies as SkipRetry. Lifecycle state is persisted inside the
// service, before the router adds its Asynq wrapper.
func processingRunErrorTerminal(err error) bool {
	if errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, types.ErrInvalidPaperInfoTaskPayload) ||
		errors.Is(err, types.ErrInvalidPaperExtraction) ||
		errors.Is(err, types.ErrInvalidPaperCatalogData) ||
		errors.Is(err, ErrInvalidPaperMetadata) ||
		errors.Is(err, ErrPaperCatalogNoMatch) ||
		errors.Is(err, ErrPaperInfoCatalogUnavailable) ||
		errors.Is(err, ErrStalePaperInfo) {
		return true
	}
	var retryable interface{ TaskRetryable() bool }
	return errors.As(err, &retryable) && !retryable.TaskRetryable()
}
