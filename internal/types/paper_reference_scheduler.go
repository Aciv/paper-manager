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
	Completed        bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate checks the durable ownership tuple of a reference job.
func (job PaperReferenceJob) Validate() error {
	if job.TargetDocumentID == uuid.Nil {
		return fmt.Errorf("%w: reference target document_id is required", ErrInvalidPaperCatalogData)
	}
	return nil
}

// PaperReferenceOutcome is the terminal result reported for one provider
// attempt. The scheduler decides whether another provider should be queued.
type PaperReferenceOutcome string

const (
	PaperReferenceOutcomeMatched          PaperReferenceOutcome = "matched"
	PaperReferenceOutcomeNoMatch          PaperReferenceOutcome = "no_match"
	PaperReferenceOutcomeRetryableFailure PaperReferenceOutcome = "retryable_failure"
	PaperReferenceOutcomeTerminalFailure  PaperReferenceOutcome = "terminal_failure"
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
	return claim.Provider != "" && !claim.Job.Completed
}

// PaperReferenceReport describes what the worker should do after reporting a
// provider attempt. Requeue means the same small task should be enqueued again
// so the scheduler can choose the next global provider turn.
type PaperReferenceReport struct {
	Requeue   bool
	Completed bool
}

// PaperReferenceProgress is a bounded citation-target projection. It is
// populated from PostgreSQL document source/stage state, never scheduler jobs.
type PaperReferenceProgress struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}
