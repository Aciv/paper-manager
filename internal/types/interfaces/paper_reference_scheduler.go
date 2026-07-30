package interfaces

import (
	"context"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// PaperReferenceScheduler owns global reference-resolution scheduling state.
// It keeps run ownership, provider history, cancellation, and the shared
// provider rotation outside short-lived Asynq payloads.
type PaperReferenceScheduler interface {
	// Register creates or refreshes one target job. It returns true only when a
	// catalog task should be queued; duplicate active jobs return false.
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
	// CancelRun marks all jobs belonging to the source/run tuple cancelled and
	// returns their target IDs so queued tasks can be removed best-effort.
	CancelRun(ctx context.Context, sourceDocumentID, runID uuid.UUID) ([]uuid.UUID, error)
	// IsRunActive reports whether a run remains eligible for provider work.
	IsRunActive(ctx context.Context, sourceDocumentID, runID uuid.UUID) (bool, error)
	// IsTargetActive checks whether at least one non-cancelled owner still needs
	// a target while an external provider call is in flight.
	IsTargetActive(ctx context.Context, targetDocumentID uuid.UUID) (bool, error)
	// Progress returns compact reference-resolution state for one source
	// document across its currently retained scheduler jobs.
	Progress(ctx context.Context, sourceDocumentID uuid.UUID) (types.PaperReferenceProgress, error)
}
