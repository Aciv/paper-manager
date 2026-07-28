package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PaperMetadataExtractor obtains paper metadata candidates from parsed
// Markdown and source-map Blocks. Implementations must stop work when ctx is
// cancelled and must not perform external catalog lookups or database writes.
type PaperMetadataExtractor interface {
	// Extract returns candidates and evidence for one exact Markdown revision.
	Extract(
		ctx context.Context,
		request *types.PaperExtractionRequest,
	) (*types.PaperExtractionResult, error)
}
