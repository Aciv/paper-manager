package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// PaperMetadataRepository owns persistence and indexed bibliographic search.
// It never changes source-file fields in documents.
type PaperMetadataRepository interface {
	// GetByDocumentID returns one document's paper metadata.
	GetByDocumentID(ctx context.Context, documentID uuid.UUID) (types.PaperMetadata, error)
	// Upsert applies a bibliographic patch, creating the metadata row when it
	// does not exist yet.
	Upsert(ctx context.Context, documentID uuid.UUID, update types.PaperMetadataUpdate) (types.PaperMetadata, error)
	// Search returns title/tag matches independently of source-document search.
	Search(ctx context.Context, search types.PaperSearch) (types.PaperMetadataPage, error)
}
