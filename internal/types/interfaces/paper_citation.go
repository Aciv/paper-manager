package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// PaperCitationRepository persists citation relationships between documents.
// The citing document contains the bibliography; the cited document is either
// a stored paper or an unknown bibliographic placeholder.
type PaperCitationRepository interface {
	// AddCitation upserts one edge with its raw extraction evidence. Repeating
	// the same edge refreshes its raw reference without creating a duplicate.
	AddCitation(ctx context.Context, citation types.PaperCitation) error
	// ReplaceCitations atomically replaces all outgoing edges for one document.
	// Each citation carries the raw extraction reference used to resolve it.
	// An empty citations slice clears the document's references.
	ReplaceCitations(ctx context.Context, citingDocumentID uuid.UUID, citations []types.PaperCitation) error
	// RemoveCitation removes one citation edge. It returns sql.ErrNoRows when
	// the relationship does not exist.
	RemoveCitation(ctx context.Context, citingDocumentID, citedDocumentID uuid.UUID) error
	// RepointCitation atomically replaces one cited endpoint while preserving
	// the raw extraction reference. It returns sql.ErrNoRows when the old edge
	// no longer exists.
	RepointCitation(
		ctx context.Context,
		citingDocumentID, oldCitedDocumentID, newCitedDocumentID uuid.UUID,
		rawReference string,
	) error
	// ListByCitingDocumentID returns papers referenced by the given document,
	// newest relationships first.
	ListByCitingDocumentID(ctx context.Context, citingDocumentID uuid.UUID) ([]types.PaperCitation, error)
	// ListByCitedDocumentID returns papers that reference the given document,
	// newest relationships first.
	ListByCitedDocumentID(ctx context.Context, citedDocumentID uuid.UUID) ([]types.PaperCitation, error)
}
