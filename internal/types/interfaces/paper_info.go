package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PaperInfoService orchestrates paper information extraction and catalog
// resolution. Its typed commands keep application code independent of Asynq.
type PaperInfoService interface {
	// ExtractMetadata loads one Markdown revision, invokes Python extraction,
	// and writes its current metadata and raw-reference snapshot.
	ExtractMetadata(
		ctx context.Context,
		command types.PaperMetadataExtractCommand,
	) (catalogResolvable bool, err error)
	// PaperSourceQueryEnqueue starts the source_lookup stage, builds the
	// provider-neutral query, and submits one source job to the in-process
	// catalog dispatcher. Provider HTTP and candidate application happen in the
	// dispatcher and QueryApply respectively.
	PaperSourceQueryEnqueue(
		ctx context.Context,
		command types.PaperSourceQueryEnqueueCommand,
	) error
	// ReferenceQueryEnqueue reloads persisted citations for one Markdown revision,
	// deduplicates unresolved rows, then submits provider-owned catalog jobs to
	// the in-process dispatcher. It does not call providers.
	ReferenceQueryEnqueue(
		ctx context.Context,
		command types.PaperReferenceQueryEnqueueCommand,
	) error
	// QueryApply persists one provider result emitted by the catalog
	// dispatcher. It is idempotent and generation-fenced.
	QueryApply(ctx context.Context, command types.PaperQueryApplyCommand) error
	// RetryExtraction enqueues extraction for the current durable Markdown
	// revision. It rejects documents without an available source file, unparsed
	// documents, and active span attempts.
	RetryExtraction(ctx context.Context, documentID string) error
	// RetryCatalogResolution enqueues only source-paper catalog lookup from the
	// uploaded document's current metadata.
	RetryCatalogResolution(ctx context.Context, documentID string) error
	// RetryReferenceResolution requeues only the citation dispatcher for the
	// source document's current Markdown revision.
	RetryReferenceResolution(ctx context.Context, documentID string) error
	// Cancel stops queued/active paper-information work without changing a
	// ready document's parse lifecycle state.
	Cancel(ctx context.Context, documentID string) error
}
