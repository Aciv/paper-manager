package service

import (
	"context"
	"errors"
	"fmt"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type workflowCancellationService struct {
	runs      interfaces.ProcessingRunRepository
	tasks     interfaces.TaskInspector
	scheduler interfaces.PaperReferenceScheduler
}

func NewWorkflowCancellationService(
	runs interfaces.ProcessingRunRepository,
	tasks interfaces.TaskInspector,
	scheduler interfaces.PaperReferenceScheduler,
) interfaces.WorkflowCancellationService {
	return &workflowCancellationService{runs: runs, tasks: tasks, scheduler: scheduler}
}

func (s *workflowCancellationService) CancelActiveRuns(
	ctx context.Context, ownerDocumentID uuid.UUID, kind types.ProcessingWorkflowKind,
) (types.WorkflowCancellationResult, error) {
	result := types.WorkflowCancellationResult{OwnerDocumentID: ownerDocumentID, WorkflowKind: kind, RunIDs: []uuid.UUID{}}
	if ownerDocumentID == uuid.Nil {
		return result, errors.New("processing run owner document id is required")
	}
	if !cancellableWorkflowKind(kind) {
		return result, fmt.Errorf("processing workflow %q is not cancellable", kind)
	}
	if s == nil || s.runs == nil {
		return result, errors.New("processing run repository is not configured")
	}
	reason := cancellationReason(kind)
	runs, err := s.runs.RequestCancelActive(context.WithoutCancel(ctx), ownerDocumentID, kind,
		"USER_CANCELLED", reason)
	if err != nil {
		return result, err
	}
	var cleanupErrors []error
	for _, run := range runs {
		result.RunIDs = append(result.RunIDs, run.ID)
		if s.tasks != nil {
			deleted, signalled, queueErr := s.tasks.CancelTasksForRun(
				context.WithoutCancel(ctx), ownerDocumentID.String(), run.ID.String(), kind)
			result.DeletedTasks += deleted
			result.SignalledTasks += signalled
			if queueErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("cancel queue tasks for run %s: %w", run.ID, queueErr))
			}
		}
		if _, _, finishErr := s.runs.Finish(context.WithoutCancel(ctx), run.ID,
			types.ProcessingRunCancelled, "USER_CANCELLED", reason); finishErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("finish cancelled run %s: %w", run.ID, finishErr))
		}
	}
	err = errors.Join(cleanupErrors...)
	if err != nil {
		logger.Warn(ctx, "workflow cancellation completed with cleanup failures",
			"document_id", ownerDocumentID, "workflow", kind, "run_count", len(result.RunIDs),
			"deleted_tasks", result.DeletedTasks, "signalled_tasks", result.SignalledTasks, "error", err)
		return result, err
	}
	logger.Info(ctx, "workflow cancellation completed",
		"document_id", ownerDocumentID, "workflow", kind, "run_count", len(result.RunIDs),
		"deleted_tasks", result.DeletedTasks, "signalled_tasks", result.SignalledTasks,
		"released_targets", result.ReleasedTargets)
	return result, nil
}

func cancellableWorkflowKind(kind types.ProcessingWorkflowKind) bool {
	switch kind {
	case types.ProcessingWorkflowDocument,
		types.ProcessingWorkflowCatalogReference,
		types.ProcessingWorkflowReferenceDownload:
		return true
	default:
		return false
	}
}

func cancellationReason(kind types.ProcessingWorkflowKind) string {
	switch kind {
	case types.ProcessingWorkflowCatalogReference:
		return "reference catalog run cancelled by user"
	case types.ProcessingWorkflowReferenceDownload:
		return "reference PDF acquisition cancelled by user"
	default:
		return "document processing cancelled by user"
	}
}

var _ interfaces.WorkflowCancellationService = (*workflowCancellationService)(nil)
