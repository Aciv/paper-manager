package interfaces

import (
	"context"
	"paperMaster/internal/types"
	"time"
)

type TaskDeadLetterRepository interface {
	// Insert persists a task after its retry budget is exhausted.
	Insert(context.Context, *types.TaskDeadLetter) error
	// ListByScope returns newest-first dead letters for a Scope/ScopeID pair.
	// cursor is the stringified ID of the oldest row from the previous page;
	// an empty cursor starts at the newest row. The limit is clamped to 1..200.
	ListByScope(context.Context, string, string, string, int) ([]*types.TaskDeadLetter, string, error)
	// ListByTaskType returns newest-first dead letters for a task type.
	// It uses the same cursor and limit semantics as ListByScope.
	ListByTaskType(context.Context, string, string, int) ([]*types.TaskDeadLetter, string, error)
	// DeleteByID removes one dead-letter row after the task has been handled.
	DeleteByID(context.Context, int64) error
	// LatestFailureAt returns the newest terminal failure for one task type and
	// scope tuple. A nil timestamp means no matching dead letter exists.
	LatestFailureAt(
		ctx context.Context,
		scope string,
		scopeID string,
		taskType string,
	) (*time.Time, error)
}
