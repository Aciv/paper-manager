package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// DocumentBaseRepository defines persistence operations for collections and
// their source-document memberships.
type DocumentBaseRepository interface {
	BibliographySnapshotRepository
	// CreateBase inserts a named document base.
	CreateBase(ctx context.Context, base types.DocumentBase) (types.DocumentBase, error)
	// GetBase retrieves one base by UUID.
	GetBase(ctx context.Context, id uuid.UUID) (types.DocumentBase, error)
	// ListBases returns every base ordered by creation time descending.
	ListBases(ctx context.Context) ([]types.DocumentBase, error)
	// UpdateBase changes a base's name and description.
	UpdateBase(ctx context.Context, id uuid.UUID, name, description string) (types.DocumentBase, error)
	// DeleteBase removes a base and its memberships, leaving source documents intact.
	DeleteBase(ctx context.Context, id uuid.UUID) error
	// AddDocument adds a source document to a base. Repeating the request is idempotent.
	AddDocument(ctx context.Context, membership types.DocumentBaseFile) error
	// RemoveDocument removes one membership without deleting the source document.
	RemoveDocument(ctx context.Context, baseID, documentID uuid.UUID) error
	// ListDocuments returns paginated source documents that belong to a base.
	ListDocuments(ctx context.Context, baseID uuid.UUID, page, pageSize int, excludeUnknown bool) (types.DocumentPage, error)
}

// DocumentBaseService validates and coordinates document-base operations.
type DocumentBaseService interface {
	// CreateBase creates a named collection.
	CreateBase(ctx context.Context, name, description string) (*types.DocumentBase, error)
	// GetBase retrieves a collection by its text UUID.
	GetBase(ctx context.Context, id string) (*types.DocumentBase, error)
	// ListBases lists all collections.
	ListBases(ctx context.Context) ([]types.DocumentBase, error)
	// UpdateBase changes a collection's mutable fields.
	UpdateBase(ctx context.Context, id string, name, description *string) (*types.DocumentBase, error)
	// DeleteBase removes only the collection and its memberships.
	DeleteBase(ctx context.Context, id string) error
	// AddDocumentToBase adds a source document to the collection.
	AddDocumentToBase(ctx context.Context, baseID, documentID string) error
	// RemoveDocumentFromBase removes one collection membership.
	RemoveDocumentFromBase(ctx context.Context, baseID, documentID string) error
	// ListBaseDocuments lists collection members with normal document metadata.
	ListBaseDocuments(ctx context.Context, baseID string, page int, excludeUnknown bool) (types.DocumentPage, error)
}
