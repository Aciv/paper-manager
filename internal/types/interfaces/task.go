package interfaces

import (
	"context"

	"github.com/hibiken/asynq"
)

// TaskEnqueuer is the context-aware queue boundary used by application
// services. *asynq.Client satisfies it directly; alternative implementations
// may execute tasks synchronously without changing service constructors.
type TaskEnqueuer interface {
	EnqueueContext(
		ctx context.Context,
		task *asynq.Task,
		opts ...asynq.Option,
	) (*asynq.TaskInfo, error)
}

// TaskHandler is a interface for handling asynchronous tasks
type TaskHandler interface {
	// Handle handles the task
	Handle(ctx context.Context, t *asynq.Task) error
}
