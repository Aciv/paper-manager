package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PaperEnrichmentService orchestrates candidate extraction and catalog
// resolution. Its typed commands keep application code independent of Asynq.
type PaperEnrichmentService interface {
	// ExtractMetadata loads one Markdown revision, invokes Python extraction,
	// persists its candidates, and schedules catalog resolution when usable.
	ExtractMetadata(
		ctx context.Context,
		command types.PaperMetadataExtractCommand,
	) error
	// ResolveMetadata loads one persisted extraction, queries configured
	// catalogs, ranks candidates, and conditionally updates canonical metadata.
	ResolveMetadata(
		ctx context.Context,
		command types.PaperCatalogResolveCommand,
	) error
}

// PaperEnrichmentTaskEnqueuer schedules the two paper-enrichment stages
// without exposing a concrete queue client to application services.
type PaperEnrichmentTaskEnqueuer interface {
	// EnqueueMetadataExtraction schedules Python candidate extraction.
	EnqueueMetadataExtraction(
		ctx context.Context,
		payload types.PaperMetadataExtractPayload,
	) error
	// EnqueueCatalogResolution schedules external scholarly catalog lookup.
	EnqueueCatalogResolution(
		ctx context.Context,
		payload types.PaperCatalogResolvePayload,
	) error
}
