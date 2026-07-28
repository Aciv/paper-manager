package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PaperService is the handler-facing boundary for canonical bibliographic
// metadata and citation relationships. It does not own source-file parsing or
// external catalog orchestration.
type PaperService interface {
	// GetMetadata retrieves one document's bibliographic metadata.
	GetMetadata(ctx context.Context, documentID string) (*types.PaperMetadata, error)
	// UpdateMetadata applies a validated partial bibliographic update.
	UpdateMetadata(
		ctx context.Context,
		documentID string,
		update types.PaperMetadataUpdate,
	) (*types.PaperMetadata, error)
	// SearchMetadata searches title, author, and discipline tags.
	SearchMetadata(ctx context.Context, search types.PaperSearch) (types.PaperMetadataPage, error)
	// AddCitation adds a matched citation or creates/reuses an unknown
	// placeholder when input.CitedDocumentID is nil.
	AddCitation(
		ctx context.Context,
		citingDocumentID string,
		input types.PaperCitationInput,
	) (*types.PaperCitation, error)
	// ReplaceCitations replaces every outgoing citation for one paper,
	// including unresolved references represented by nil target IDs.
	ReplaceCitations(
		ctx context.Context,
		citingDocumentID string,
		inputs []types.PaperCitationInput,
	) ([]types.PaperCitation, error)
	// RemoveCitation removes one outgoing edge and cleans up an unreferenced
	// unknown target.
	RemoveCitation(ctx context.Context, citingDocumentID, citedDocumentID string) error
	// ListCitations returns papers referenced by documentID.
	ListCitations(ctx context.Context, documentID string) ([]types.PaperCitation, error)
	// ListCitedBy returns papers that reference documentID.
	ListCitedBy(ctx context.Context, documentID string) ([]types.PaperCitation, error)
	// ResolveCitation repoints the unknown edge derived from rawReference to a
	// matched stored document, then removes the unused placeholder.
	ResolveCitation(
		ctx context.Context,
		citingDocumentID, resolvedDocumentID, rawReference string,
	) (*types.PaperCitation, error)
}
