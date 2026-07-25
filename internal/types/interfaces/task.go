package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/hibiken/asynq"
)

// DocumentTaskEnqueuer schedules document recognition without coupling the
// application service to a specific queue backend.
type DocumentTaskEnqueuer interface {
	EnqueueDocumentProcess(ctx context.Context, payload types.DocumentProcessPayload) error
}

// TaskHandler is a interface for handling asynchronous tasks
type TaskHandler interface {
	// Handle handles the task
	Handle(ctx context.Context, t *asynq.Task) error
}
