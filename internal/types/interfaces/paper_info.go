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
	// ResolveMetadata reloads canonical metadata and unresolved references,
	// queries configured catalogs, ranks candidates, and applies selected data.
	// Source-backed documents use a Markdown revision guard; unknown
	// placeholders resolve directly from metadata or incoming raw references.
	ResolveMetadata(
		ctx context.Context,
		command types.PaperCatalogResolveCommand,
	) error
	// GetStatus returns the current safely observable queue/provenance state for
	// extraction and catalog work. It is independent from document parsing.
	GetStatus(ctx context.Context, documentID string) (types.PaperInfoStatus, error)
	// RetryExtraction enqueues extraction for the current durable Markdown
	// revision. It rejects source-free placeholders and unparsed documents.
	RetryExtraction(ctx context.Context, documentID string) (types.PaperInfoStatus, error)
	// RetryCatalogResolution enqueues catalog lookup from current metadata or an
	// unresolved incoming reference. It rejects deleting documents and empty
	// lookup identities.
	RetryCatalogResolution(ctx context.Context, documentID string) (types.PaperInfoStatus, error)
}
