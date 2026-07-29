package interfaces

import "context"

// TaskInspector abstracts queue inspection / cancellation against the
// task backend. It is best-effort: implementations may scan a finite
// number of tasks per call and return whatever count they could
// affect.
//
// Use cases today: user-initiated cancellation of a document parse. It removes
// queued document tasks and signals active workers to stop at their next
// checkpoint.
type TaskInspector interface {
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
}
