package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PaperCatalogProvider adapts one external scholarly catalog to normalized Go
// query and candidate types. Implementations own HTTP authentication,
// provider-specific pagination, and response normalization.
type PaperCatalogProvider interface {
	// Name returns the stable provider key used in configuration and provenance.
	Name() string
	// Search returns at most query.EffectiveLimit normalized candidates.
	Search(
		ctx context.Context,
		query types.PaperLookupQuery,
	) ([]types.PaperCatalogCandidate, error)
}
