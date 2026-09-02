package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// DocumentSelectionProcessor accepts one already-validated, transient PDF
// selection. Implementations must honor ctx cancellation and must not persist
// text or screenshot bytes.
type DocumentSelectionProcessor interface {
	ProcessSelection(ctx context.Context, input types.DocumentSelectionInput) (types.DocumentSelectionResult, error)
}
