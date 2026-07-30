package types

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PaperReferenceJob is the scheduler-owned state for resolving one unknown
// cited-paper placeholder. It deliberately lives outside Asynq payloads so a
// queue message can remain a stable, single-document command.
type PaperReferenceJob struct {
	TargetDocumentID uuid.UUID
	TriedProviders   []PaperCatalogProvider
	Provider         PaperCatalogProvider
	InFlight         bool
	Cancelled        bool
	Completed        bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Owners           []PaperReferenceOwner
}

// PaperReferenceOwner records one source/run that depends on a shared unknown
// placeholder. A target can be cited by several uploaded papers, so cancelling
// one run must not cancel work still needed by another owner.
type PaperReferenceOwner struct {
	SourceDocumentID uuid.UUID
	RunID            uuid.UUID
	// Attempt distinguishes an automatic dispatch from a later operator retry
	// for the same source/run pair. It is used only for trace correlation.
	Attempt int
	// RequestID preserves HTTP-to-background-work correlation without putting
	// request ownership on the shared target job itself.
	RequestID string
	Cancelled bool
}

// Validate checks the durable ownership tuple of a reference job.
func (job PaperReferenceJob) Validate() error {
	if job.TargetDocumentID == uuid.Nil {
		return fmt.Errorf("%w: reference target document_id is required", ErrInvalidPaperCatalogData)
	}
	if len(job.Owners) == 0 {
		return fmt.Errorf("%w: reference job needs an owner", ErrInvalidPaperCatalogData)
	}
	for _, owner := range job.Owners {
		if owner.SourceDocumentID == uuid.Nil {
			return fmt.Errorf("%w: reference source document_id is required", ErrInvalidPaperCatalogData)
		}
		if owner.RunID == uuid.Nil {
			return fmt.Errorf("%w: reference run_id is required", ErrInvalidPaperCatalogData)
		}
		if job.TargetDocumentID == owner.SourceDocumentID {
			return fmt.Errorf("%w: reference target must differ from source", ErrInvalidPaperCatalogData)
		}
		if owner.Attempt < 0 {
			return fmt.Errorf("%w: reference attempt cannot be negative", ErrInvalidPaperCatalogData)
		}
	}
	return nil
}

// ActiveOwner returns one owner that still needs this shared target. The
// caller must use it only for attribution such as a per-source limiter; it is
// never the authoritative owner of the job because every active owner shares
// the same lookup result.
func (job PaperReferenceJob) ActiveOwner() (PaperReferenceOwner, bool) {
	for _, owner := range job.Owners {
		if !owner.Cancelled {
			return owner, true
		}
	}
	return PaperReferenceOwner{}, false
}

// PaperReferenceOutcome is the terminal result reported for one provider
// attempt. The scheduler decides whether another provider should be queued.
type PaperReferenceOutcome string

const (
	PaperReferenceOutcomeMatched          PaperReferenceOutcome = "matched"
	PaperReferenceOutcomeNoMatch          PaperReferenceOutcome = "no_match"
	PaperReferenceOutcomeRetryableFailure PaperReferenceOutcome = "retryable_failure"
	PaperReferenceOutcomeTerminalFailure  PaperReferenceOutcome = "terminal_failure"
	PaperReferenceOutcomeCancelled        PaperReferenceOutcome = "cancelled"
)

// PaperReferenceClaim is an exclusive permission to query one provider for a
// job. Provider is empty if the job is cancelled, already complete, or every
// configured provider has been tried.
type PaperReferenceClaim struct {
	Job      PaperReferenceJob
	Provider PaperCatalogProvider
}

// Ready reports whether a provider request should be made for this claim.
func (claim PaperReferenceClaim) Ready() bool {
	return claim.Provider != "" && !claim.Job.Cancelled && !claim.Job.Completed
}

// PaperReferenceReport describes what the worker should do after reporting a
// provider attempt. Requeue means the same small task should be enqueued again
// so the scheduler can choose the next global provider turn.
type PaperReferenceReport struct {
	Requeue   bool
	Completed bool
	Cancelled bool
}

// PaperReferenceProgress is the compact, source-document-scoped projection
// used by the processing API. Detailed provider attempts remain in spans.
type PaperReferenceProgress struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Cancelled int `json:"cancelled"`
}
