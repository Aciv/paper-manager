package types

import "github.com/google/uuid"

// WorkflowCancellationResult is the control-plane result of cancelling all
// active runs for one owner/workflow tuple. Queue counts are best-effort; the
// durable run fence is authoritative even when adapter cleanup reports errors.
type WorkflowCancellationResult struct {
	OwnerDocumentID uuid.UUID
	WorkflowKind    ProcessingWorkflowKind
	RunIDs          []uuid.UUID
	DeletedTasks    int
	SignalledTasks  int
	ReleasedTargets int
}
