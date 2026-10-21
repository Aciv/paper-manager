package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type DOICitationClient interface {
	FetchBibTeX(ctx context.Context, doi string) (types.DOICitation, error)
}

type BibliographyService interface {
	EnqueueBaseBibliography(ctx context.Context, baseID, requestID string) (types.BibliographyTask, error)
	GetBaseBibliographyTask(ctx context.Context, baseID, taskID string) (types.BibliographyTask, error)
	ProcessBaseBibliography(ctx context.Context, task *asynq.Task) error
}

type BibliographySnapshotRepository interface {
	GetBibliographySnapshot(ctx context.Context, baseID uuid.UUID, maxEntries int) (types.BibliographySnapshotSource, error)
}
