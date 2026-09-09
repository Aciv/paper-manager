package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// WorkflowCancellationService applies the durable database fence before
// invoking workflow-specific queue and scheduler cleanup.
type WorkflowCancellationService interface {
	CancelActiveRuns(ctx context.Context, ownerDocumentID uuid.UUID, kind types.ProcessingWorkflowKind) (types.WorkflowCancellationResult, error)
}
