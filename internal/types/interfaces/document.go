package interfaces

import (
	"context"
	"io"
	"mime/multipart"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// DocumentRepository defines persistence operations for documents.
type DocumentRepository interface {
	// Create inserts one source-document record and returns it.
	Create(ctx context.Context, document types.Document) (types.Document, error)
	// Get retrieves one document by UUID.
	Get(ctx context.Context, id uuid.UUID) (types.Document, error)
	// EnsureUnknown creates an internal bibliographic placeholder when absent
	// and returns whether this call inserted it. A conflicting non-placeholder
	// row with the same deterministic ID is an error.
	EnsureUnknown(ctx context.Context, id uuid.UUID) (document types.Document, created bool, err error)
	// DeleteUnknownIfUnreferenced deletes an unknown placeholder only when no
	// citation points from or to it. The boolean reports whether it was deleted.
	DeleteUnknownIfUnreferenced(ctx context.Context, id uuid.UUID) (deleted bool, err error)
	// Search returns a paginated document result using ID or DOI filters.
	Search(ctx context.Context, search types.DocumentSearch) (types.DocumentPage, error)
	// GetMarkdown returns the current parsed Markdown source for a document.
	// It returns sql.ErrNoRows when the document has not been parsed yet.
	GetMarkdown(ctx context.Context, documentID uuid.UUID) (types.DocumentMarkdown, error)
	// ReplaceParsedOutput atomically replaces a document's current Markdown
	// manifest and source-map Blocks, then marks a processing document ready.
	// It returns types.ErrDocumentNotProcessing if cancellation/deletion won
	// the status race before the result could be committed.
	ReplaceParsedOutput(ctx context.Context, markdown types.DocumentMarkdown, blocks []types.Block) error
	// ListBlocks returns a document's persisted source-map blocks. pageNumber
	// of zero returns all pages; a positive value returns one PDF page.
	ListBlocks(ctx context.Context, documentID uuid.UUID, pageNumber int) ([]types.Block, error)
	// GetBlock returns one source-map block belonging to documentID.
	GetBlock(ctx context.Context, documentID, blockID uuid.UUID) (types.Block, error)
	// Delete removes a document. Database rows with document foreign keys are
	// removed by ON DELETE CASCADE; non-relational diagnostics are handled by
	// the repository transaction.
	Delete(ctx context.Context, id uuid.UUID) error
	// UpdateStatus changes the persisted lifecycle status and error message.
	UpdateStatus(ctx context.Context, id uuid.UUID, status types.DocumentStatus, message *string) error
	// UpdateStatusIfCurrent changes status only when its current value is one
	// of expectedStatuses. It returns false without an error when no row matches.
	UpdateStatusIfCurrent(ctx context.Context, id uuid.UUID, status types.DocumentStatus, message *string, expectedStatuses []types.DocumentStatus) (bool, error)
	// UpdateDocumentInfo changes original-file metadata. Nil fields keep their
	// stored values; paper metadata is owned by PaperMetadataRepository.
	UpdateDocumentInfo(ctx context.Context, id uuid.UUID, originalFilename, doi *string) (types.Document, error)
	// SetDOIIfEmpty atomically records a normalized provider DOI only while the
	// current value is blank. It returns false when a user or another worker
	// supplied a DOI first, and returns ErrDuplicateDOI if another document owns
	// the provider DOI.
	SetDOIIfEmpty(ctx context.Context, id uuid.UUID, doi string) (bool, error)
}

// DocumentService contains document lifecycle and storage orchestration.
type DocumentService interface {
	// CreateDocumentFromFile stores an upload and creates its source record.
	CreateDocumentFromFile(ctx context.Context, file *multipart.FileHeader, doi string) (*types.Document, error)
	// GetDocumentByID parses and retrieves a document by its textual UUID.
	GetDocumentByID(ctx context.Context, id string) (*types.Document, error)
	// GetDocumentStatus returns the current lifecycle status for a document.
	GetDocumentStatus(ctx context.Context, id string) (types.DocumentStatus, error)
	// SetDocumentStatus updates a document lifecycle status and optional error message.
	SetDocumentStatus(ctx context.Context, id string, status types.DocumentStatus, message *string) error
	// UpdateDocumentInfo updates original-file and DOI fields. Nil fields keep
	// their stored value unchanged.
	UpdateDocumentInfo(ctx context.Context, id string, originalFilename, doi *string) (*types.Document, error)
	// FailDocumentIfProcessing marks a queued or processing document as failed.
	// It does not overwrite a newer ready, failed, or deleting state.
	FailDocumentIfProcessing(ctx context.Context, id string, message string) (bool, error)
	// CancelDocumentParse cancels a queued or active document parse. It is
	// idempotent for already-cancelled documents and best-effort dequeues work.
	CancelDocumentParse(ctx context.Context, id string) (*types.Document, error)
	// RetryDocumentParse queues a new recognition attempt after a failed or
	// cancelled attempt. Active parsing must be cancelled rather than retried.
	RetryDocumentParse(ctx context.Context, id string) (*types.Document, error)
	// RetryDocumentProcess starts a new persistent pipeline. Ready documents
	// reuse their committed Markdown and restart at paper extraction; failed or
	// cancelled documents restart from recognition.
	RetryDocumentProcess(ctx context.Context, id string) (*types.Document, error)
	// SearchDocument performs paginated ID and DOI searches.
	SearchDocument(ctx context.Context, search types.DocumentSearch) (types.DocumentPage, error)
	// GetDocumentFile opens the stored object and returns its original filename.
	GetDocumentFile(ctx context.Context, id string) (reader io.ReadCloser, filename string, err error)
	// GetDocumentBlocks returns selectable source-map blocks, optionally scoped
	// to one one-based PDF page (zero returns every page).
	GetDocumentBlocks(ctx context.Context, id string, pageNumber int) ([]types.Block, error)
	// GetDocumentBlockImage opens a rendered image/table asset after verifying
	// that blockID belongs to documentID.
	GetDocumentBlockImage(ctx context.Context, documentID, blockID string) (reader io.ReadCloser, filename string, err error)
	// DeleteDocument removes the stored object and metadata for one document.
	DeleteDocument(ctx context.Context, id string) error
	// DeleteDocumentList removes multiple documents sequentially.
	DeleteDocumentList(ctx context.Context, ids []string) error

	// ProcessDocument advances one persistent document pipeline. It chooses and
	// queues the next incomplete stage but never performs recognition itself.
	ProcessDocument(ctx context.Context, task *asynq.Task) error
	// RecognizeDocument performs only the asynchronous recognition stage.
	RecognizeDocument(ctx context.Context, task *asynq.Task) error
}
