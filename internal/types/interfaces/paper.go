package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// PaperService is the handler-facing boundary for canonical bibliographic
// metadata and citation relationships. It does not own source-file parsing or
// external catalog orchestration.
type PaperService interface {
	// GetMetadata retrieves one document's bibliographic metadata.
	GetMetadata(ctx context.Context, documentID string) (*types.PaperMetadata, error)
	// GetMetadataBatch loads metadata for many existing documents in one query.
	// Missing metadata rows are absent from the map.
	GetMetadataBatch(
		ctx context.Context, documentIDs []uuid.UUID,
	) (map[uuid.UUID]types.PaperMetadata, error)
	// UpdateMetadata applies a validated partial bibliographic update. Calls
	// without an internal MetadataSource are treated as manual edits.
	UpdateMetadata(
		ctx context.Context,
		documentID string,
		update types.PaperMetadataUpdate,
	) (*types.PaperMetadata, error)
	// SearchMetadata searches title, author, and discipline tags.
	SearchMetadata(ctx context.Context, search types.PaperSearch) (types.PaperMetadataPage, error)
	// AddCitation adds one bibliography entry to the citing paper. The row is
	// owned by that paper; an identical raw reference in another paper is a
	// separate row.
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
	// MergeExtractedCitations reconciles a positively detected bibliography.
	// It preserves manual edges and links references that already match an
	// available library document by DOI or title.
	MergeExtractedCitations(
		ctx context.Context,
		citingDocumentID string,
		inputs []types.PaperCitationInput,
	) ([]types.PaperCitation, error)
	// RemoveCitation removes one outgoing edge by reference ID.
	RemoveCitation(ctx context.Context, citingDocumentID, referenceID string) error
	// DeleteReference removes one reference row by reference ID. It resolves
	// the owning citing document internally; no document ID is required.
	DeleteReference(ctx context.Context, referenceID string) error
	// UpdateReferenceSource applies a manual edit to one reference's
	// bibliographic fields. Manual ownership fences stale resolution results.
	UpdateReferenceSource(
		ctx context.Context,
		referenceID string,
		update types.PaperReferenceSourceUpdate,
	) (*types.PaperReference, error)
	// ListCitations returns references used by documentID.
	ListCitations(ctx context.Context, documentID string) ([]types.PaperCitation, error)
	// ListCitationsPage provides bounded bibliography traversal for background
	// dispatchers without changing the existing handler-facing list operation.
	ListCitationsPage(ctx context.Context, documentID string, offset, limit int) (types.PaperCitationPage, error)
	// ListCitedBy returns citations whose resolved reference points to documentID.
	ListCitedBy(ctx context.Context, documentID string) ([]types.PaperCitation, error)
	// ResolveCitation links one raw bibliography entry of a citing paper to a
	// stored document.
	ResolveCitation(
		ctx context.Context,
		citingDocumentID, resolvedDocumentID, rawReference string,
	) (*types.PaperCitation, error)
	// GetReference returns one citation row by reference ID.
	GetReference(ctx context.Context, referenceID string) (*types.PaperReference, error)
	// BeginReferenceDownload records a new explicit download attempt. It
	// returns ErrReferenceAlreadyResolved when the reference is linked.
	BeginReferenceDownload(ctx context.Context, referenceID, attemptID string) (*types.PaperReference, error)
	// FailReferenceDownload records a terminal failure for one attempt.
	FailReferenceDownload(ctx context.Context, referenceID, attemptID, errorCode, errorMessage string) error
	// ResolveReference manually links one citation row to a stored document.
	ResolveReference(ctx context.Context, referenceID, resolvedDocumentID string) (*types.PaperReference, error)
	// RematchReference retries local DOI/title matching for one citation row
	// against the current library. An existing link is returned unchanged and
	// no external provider is called.
	RematchReference(ctx context.Context, referenceID string) (*types.PaperReference, bool, error)
	// LinkReferenceDocument links one citation row; it is used by local
	// matching, workers, and reference PDF import.
	LinkReferenceDocument(
		ctx context.Context, referenceID, documentID uuid.UUID, overwrite bool,
	) (*types.PaperReference, error)
	// FindLocalReferenceMatch finds an available library document by DOI/title
	// while excluding the current citing document.
	FindLocalReferenceMatch(
		ctx context.Context, reference types.PaperReference, excludeDocumentID uuid.UUID,
	) (uuid.UUID, bool, error)
	// QueueReferenceResolution bumps generation and queues unresolved work.
	QueueReferenceResolution(ctx context.Context, referenceID uuid.UUID) (types.PaperReference, bool, error)
	// StartReferenceResolution fences queued -> running for one generation.
	StartReferenceResolution(ctx context.Context, referenceID uuid.UUID, generation int64) (types.PaperReference, bool, error)
	// FinishReferenceResolution writes a terminal/retry state for one generation.
	FinishReferenceResolution(
		ctx context.Context,
		referenceID uuid.UUID,
		generation int64,
		status types.DocumentStageStatus,
		provider, errorCode, errorMessage string,
		update *types.PaperReferenceMetadataPatch,
		resolvedDocumentID *uuid.UUID,
	) (types.PaperReference, bool, error)
}
