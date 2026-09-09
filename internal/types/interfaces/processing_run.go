package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// ProcessingRunRepository persists the control-plane state of asynchronous
// workflows. Every transition is atomic and returns applied=false when the
// current state does not permit the requested transition.
type ProcessingRunRepository interface {
	// CreateQueued creates one run or returns the existing row for the same
	// owner/workflow/idempotency tuple. created is true only for the insert.
	CreateQueued(ctx context.Context, run types.ProcessingRun) (stored types.ProcessingRun, created bool, err error)
	Get(ctx context.Context, id uuid.UUID) (types.ProcessingRun, error)
	// TryStart performs queued -> running. A cancelled, active, or terminal run
	// is never reopened by this operation.
	TryStart(ctx context.Context, id uuid.UUID) (run types.ProcessingRun, applied bool, err error)
	// MarkRetryQueued performs running -> queued for an Asynq retry of the same
	// logical run. Final failures must use Finish instead.
	MarkRetryQueued(ctx context.Context, id uuid.UUID, code, reason string) (run types.ProcessingRun, applied bool, err error)
	// RequestCancel performs queued/running -> cancel_requested. Repeated calls
	// are idempotent and terminal rows remain unchanged.
	RequestCancel(ctx context.Context, id uuid.UUID, code, reason string) (run types.ProcessingRun, applied bool, err error)
	// RequestCancelActive atomically fences every active run for an
	// owner/workflow tuple and cancels all active spans linked by run_id. It
	// returns both newly requested and already-requested runs for idempotent
	// adapter cleanup.
	RequestCancelActive(ctx context.Context, ownerDocumentID uuid.UUID, kind types.ProcessingWorkflowKind, code, reason string) ([]types.ProcessingRun, error)
	// Finish performs one valid transition to succeeded, failed, or cancelled.
	Finish(ctx context.Context, id uuid.UUID, state types.ProcessingRunState, code, reason string) (run types.ProcessingRun, applied bool, err error)
	// ListActive returns all queued/running/cancel_requested runs for one owner;
	// optional workflow kinds narrow the result.
	ListActive(ctx context.Context, ownerDocumentID uuid.UUID, kinds ...types.ProcessingWorkflowKind) ([]types.ProcessingRun, error)
	// ListRecent returns a bounded control-plane summary for the processing
	// view. Active runs sort before recent terminal tombstones.
	ListRecent(ctx context.Context, ownerDocumentID uuid.UUID, limit int) ([]types.ProcessingRun, error)
	// LatestAttempt returns the maximum durable attempt for one workflow.
	LatestAttempt(ctx context.Context, ownerDocumentID uuid.UUID, kind types.ProcessingWorkflowKind) (int64, error)
	// RegisterItem idempotently adds one unit to a fan-out run before enqueue.
	RegisterItem(ctx context.Context, runID uuid.UUID, itemKey string) (types.ProcessingRunItem, bool, error)
	// TryStartItem and MarkItemRetry guard retries of the same logical unit.
	TryStartItem(ctx context.Context, runID uuid.UUID, itemKey string) (types.ProcessingRunItem, bool, error)
	MarkItemRetry(ctx context.Context, runID uuid.UUID, itemKey string) error
	// FinishItem records a terminal unit and may terminally finish a sealed run.
	FinishItem(ctx context.Context, runID uuid.UUID, itemKey string, state types.ProcessingRunState, code, reason string) (types.ProcessingRun, error)
	// SealItems declares fan-out registration complete and terminally finishes
	// the run immediately when every registered unit is already terminal.
	SealItems(ctx context.Context, runID uuid.UUID) (types.ProcessingRun, error)
}
