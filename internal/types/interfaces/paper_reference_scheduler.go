package interfaces

import (
	"context"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// PaperReferenceScheduler owns global reference-resolution scheduling state.
// It keeps per-target provider history, fallback, and global cooldown outside
// short-lived Asynq payloads. Stage ownership remains on the target Document.
type PaperReferenceScheduler interface {
	// Register creates or refreshes one target job. A newer source_lookup
	// generation replaces stale scheduler state and returns true; duplicates
	// from the same generation return false.
	Register(ctx context.Context, job types.PaperReferenceJob) (queue bool, err error)
	// Claim atomically chooses the next provider for targetDocumentID. A claim
	// with Ready false must not issue an external request.
	Claim(ctx context.Context, targetDocumentID uuid.UUID) (types.PaperReferenceClaim, error)
	// Report persists one provider outcome and says whether the target should be
	// queued again for fallback. The provider must match the preceding claim.
	Report(ctx context.Context, targetDocumentID uuid.UUID, provider types.PaperCatalogProvider, outcome types.PaperReferenceOutcome) (types.PaperReferenceReport, error)
	// Cooldown temporarily removes a provider from new reference claims across
	// every run. Existing provider throttles remain the final enforcement layer.
	Cooldown(ctx context.Context, provider types.PaperCatalogProvider, delay time.Duration) error
}
