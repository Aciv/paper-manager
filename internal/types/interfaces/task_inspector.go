package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// TaskInspector abstracts queue inspection / cancellation against the
// task backend. It is best-effort: implementations may scan a finite
// number of tasks per call and return whatever count they could
// affect.
//
// Use cases today: user-initiated cancellation of a document parse. It removes
// queued document tasks and signals active workers to stop at their next
// checkpoint.
type TaskInspector interface {
	// CancelTasksForStage removes or signals only tasks carrying this exact
	// document/stage/generation envelope. The business service must persist the
	// stage cancellation fence first; this is a best-effort latency optimisation.
	CancelTasksForStage(ctx context.Context, documentID string, stage types.DocumentStage, generation int64) (deleted int, cancelled int, err error)

	// CancelTasksForDocument removes pending/scheduled/retry tasks
	// whose payload references the given document ID, and signals
	// active workers running such tasks to stop. Returns rough
	// counts of (deletedFromQueue, activeCancelled) for observability.
	// Errors are returned but callers should treat the operation as
	// best-effort: the row-level abort flag remains the source of
	// truth, this just prevents wasted work.
	CancelTasksForDocument(ctx context.Context, documentID string) (deleted int, cancelled int, err error)

	// HasQueuedTasksForDocument reports whether any pending / scheduled
	// / retry / active task referencing the given document ID still
	// lives in the queue backend. It is the read-only counterpart of
	// CancelTasksForDocument: the housekeeping sweep calls it before
	// flipping a long-idle "processing" row to "failed" so
	// it can tell a genuinely orphaned row (no task anywhere) from one
	// whose processing subtasks are merely backlogged behind a busy
	// queue (no span heartbeat yet because no worker has picked them up).
	//
	// Best-effort and short-circuiting: it returns true as soon as the
	// first match is seen. On backend error it returns (false, err);
	// callers decide the fail-safe direction. Lite mode (no Redis)
	// always returns false — inline executors never queue, so the
	// span/updated_at checks remain authoritative there.
	HasQueuedTasksForDocument(ctx context.Context, documentID string) (bool, error)

	// HasQueuedTaskTypeForDocument reports whether one specific task type for
	// documentID is pending, scheduled, retrying, or active. Recovery workers
	// use this narrower probe so a catalog task cannot mask an orphaned
	// document-recognition task (or vice versa).
	HasQueuedTaskTypeForDocument(
		ctx context.Context,
		documentID string,
		taskType string,
	) (bool, error)

	// HasQueuedTaskForStage reports whether the exact durable task envelope is
	// still pending, scheduled, retrying, or active. Stage recovery must never
	// let another generation mask an orphaned one.
	HasQueuedTaskForStage(ctx context.Context, documentID string, stage types.DocumentStage, generation int64) (bool, error)

	// HasQueuedQueryApplyForReference reports whether the exact reference
	// generation still has a pending/scheduled/retrying/active result task.
	HasQueuedQueryApplyForReference(ctx context.Context, referenceID string, generation int64) (bool, error)

	// SnapshotQueuedWork scans each queue/state at most once and returns the
	// stage/reference envelopes currently present. Housekeeping uses this to
	// keep Redis calls bounded independently of the candidate count. Complete
	// is false when the scan budget was exhausted.
	SnapshotQueuedWork(ctx context.Context) (types.QueuedWorkSnapshot, error)
}

// TaskResultInspector performs an exact task lookup for workflows that return
// a retained Asynq result. Domain services remain responsible for validating
// task type, payload ownership, and translating backend errors.
type TaskResultInspector interface {
	GetTask(ctx context.Context, queue, taskID string) (types.TaskRecord, error)
}
