package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// PaperCitationRepository persists bibliography entries. One row belongs to one
// citing document and carries its own metadata and resolution state; rows are
// never shared between citing papers.
type PaperCitationRepository interface {
	// GetReference returns one citation row by reference ID or sql.ErrNoRows.
	GetReference(ctx context.Context, id uuid.UUID) (types.PaperReference, error)
	// AddCitation upserts one manual/add path row, returning the persisted row.
	AddCitation(ctx context.Context, citation types.PaperCitation) (types.PaperCitation, error)
	// ReplaceCitations atomically replaces all bibliography rows for one
	// document.
	ReplaceCitations(ctx context.Context, citingDocumentID uuid.UUID, citations []types.PaperCitation) error
	// MergeAutomaticCitations atomically reconciles extraction-origin rows.
	// Manual and legacy rows remain untouched.
	MergeAutomaticCitations(
		ctx context.Context,
		citingDocumentID uuid.UUID,
		citations []types.PaperCitation,
	) ([]types.PaperCitation, error)
	// RemoveCitation removes one bibliography row. It returns sql.ErrNoRows when
	// the relationship does not exist.
	RemoveCitation(ctx context.Context, citingDocumentID, referenceID uuid.UUID) error
	// ListByCitingDocumentID returns references used by the given document,
	// newest relationships first.
	ListByCitingDocumentID(ctx context.Context, citingDocumentID uuid.UUID) ([]types.PaperCitation, error)
	// ListByCitingDocumentIDPage returns a stable created-at/ID ordered page.
	ListByCitingDocumentIDPage(ctx context.Context, citingDocumentID uuid.UUID, offset, limit int) (types.PaperCitationPage, error)
	// ListByCitedDocumentID returns citation rows resolved to the given document.
	ListByCitedDocumentID(ctx context.Context, citedDocumentID uuid.UUID) ([]types.PaperCitation, error)
	// QueueResolution increments the generation and queues an unresolved row.
	QueueResolution(ctx context.Context, id uuid.UUID) (types.PaperReference, bool, error)
	// StartResolution fences queued -> running for one generation.
	StartResolution(ctx context.Context, id uuid.UUID, generation int64) (types.PaperReference, bool, error)
	// FinishResolution writes a terminal state or returns a transient failure to
	// queued. When resolvedDocumentID is set, it links the row atomically.
	FinishResolution(
		ctx context.Context,
		id uuid.UUID,
		generation int64,
		status types.DocumentStageStatus,
		provider string,
		errorCode string,
		errorMessage string,
		update *types.PaperReferenceMetadataPatch,
		resolvedDocumentID *uuid.UUID,
	) (types.PaperReference, bool, error)
	// LinkResolvedDocument links one citation row to a stored document.
	// overwrite=false keeps an existing different resolution.
	LinkResolvedDocument(ctx context.Context, id uuid.UUID, documentID uuid.UUID, overwrite bool) (types.PaperReference, bool, error)
}
