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
	// GetByDocumentIDs returns metadata for many documents keyed by document ID.
	GetByDocumentIDs(
		ctx context.Context, documentIDs []uuid.UUID,
	) (map[uuid.UUID]types.PaperMetadata, error)
	// Upsert applies a bibliographic patch, creating the metadata row when it
	// does not exist yet. MetadataSource precedence is enforced atomically.
	Upsert(ctx context.Context, documentID uuid.UUID, update types.PaperMetadataUpdate) (types.PaperMetadata, error)
	// Search returns title/tag matches independently of source-document search.
	Search(ctx context.Context, search types.PaperSearch) (types.PaperMetadataPage, error)
	// SearchTitleSeeds returns metadata for available documents whose title
	// contains one of seeds, keyed by the matching seed and capped per seed.
	// It backs batched local reference matching without one query per citation.
	SearchTitleSeeds(
		ctx context.Context, seeds []string, excludeDocumentID uuid.UUID, perSeedLimit int,
	) (map[string][]types.PaperMetadata, error)
}
