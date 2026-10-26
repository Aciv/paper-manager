package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/hibiken/asynq"
)

// URLImportService uses the shared queue and inspector; no client connection
// owns the lifetime of a remote download job.
type URLImportService interface {
	EnqueueURLs(context.Context, []types.DocumentURLImportCommand, string, string) (types.URLImportTask, error)
	EnqueueURLsToBases(context.Context, []types.DocumentURLImportCommand, []string, string) (types.URLImportTask, error)
	GetURLImportTask(context.Context, string) (types.URLImportTask, error)
	ProcessURLImport(context.Context, *asynq.Task) error
}
