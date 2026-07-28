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
	) error
	// ResolveMetadata reloads canonical metadata and unresolved references,
	// queries configured catalogs, ranks candidates, and applies selected data.
	// Source-backed documents use a Markdown revision guard; unknown
	// placeholders resolve directly from metadata or incoming raw references.
	ResolveMetadata(
		ctx context.Context,
		command types.PaperCatalogResolveCommand,
	) error
}

// PaperInfoTaskEnqueuer schedules the two paper-information stages
// without exposing a concrete queue client to application services.
type PaperInfoTaskEnqueuer interface {
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
