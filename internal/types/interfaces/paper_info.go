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
	// ResolveMetadata reloads canonical metadata and unresolved citations,
	// queries configured catalogs, ranks candidates, and applies selected data.
	// Source-backed documents use a Markdown revision guard; citation rows
	// resolve directly from metadata or their stored raw reference.
	ResolveMetadata(
		ctx context.Context,
		command types.PaperCatalogResolveCommand,
	) error
	// DispatchReferences reloads persisted citations for one Markdown revision,
	// deduplicates unresolved rows, then queues provider-owned
	// catalog-resolution batches. It does not call providers.
	DispatchReferences(
		ctx context.Context,
		command types.PaperReferenceDispatchCommand,
	) error
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
