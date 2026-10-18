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
	// Search returns a paginated document result using ID or DOI filters.
	Search(ctx context.Context, search types.DocumentSearch) (types.DocumentPage, error)
	CitationTargetProgress(ctx context.Context, citingDocumentID uuid.UUID) (types.PaperReferenceProgress, error)
	// GetMarkdown returns the current parsed Markdown source for a document.
	// It returns sql.ErrNoRows when the document has not been parsed yet.
	GetMarkdown(ctx context.Context, documentID uuid.UUID) (types.DocumentMarkdown, error)
	// ReplaceParsedOutput atomically replaces a document's current Markdown
	// manifest and source-map Blocks, then marks a processing document ready.
	// It returns types.ErrDocumentNotProcessing if cancellation/deletion won
	// the status race before the result could be committed.
	ReplaceParsedOutput(ctx context.Context, markdown types.DocumentMarkdown, blocks []types.Block, markdownGeneration int64) error
	// ListBlocks returns a document's persisted source-map blocks. pageNumber
	// of zero returns all pages; a positive value returns one PDF page.
	ListBlocks(ctx context.Context, documentID uuid.UUID, pageNumber int) ([]types.Block, error)
	// GetBlock returns one source-map block belonging to documentID.
	GetBlock(ctx context.Context, documentID, blockID uuid.UUID) (types.Block, error)
	// Delete removes a document. Database rows with document foreign keys are
	// removed by ON DELETE CASCADE; non-relational diagnostics are handled by
	// the repository transaction.
	Delete(ctx context.Context, id uuid.UUID) error
	// MarkDeleting writes the source lifecycle fence before storage deletion.
	// It returns false when the document is already merged or deleting.
	MarkDeleting(ctx context.Context, id uuid.UUID) (previous types.DocumentSourceState, applied bool, err error)
	// RestoreSourceState reopens the source lifecycle only when storage cleanup
	// failed before the document row could be deleted.
	RestoreSourceState(ctx context.Context, id uuid.UUID, state types.DocumentSourceState) error
	// UpdateDocumentInfo changes original-file metadata. Nil fields keep their
	// stored values; paper metadata is owned by PaperMetadataRepository.
	UpdateDocumentInfo(ctx context.Context, id uuid.UUID, originalFilename, doi *string) (types.Document, error)
	// SetDOIIfEmpty atomically records a normalized provider DOI only while the
	// current value is blank. It returns false when a user or another worker
	// supplied a DOI first, and returns ErrDuplicateDOI if another document owns
	// the provider DOI.
	SetDOIIfEmpty(ctx context.Context, id uuid.UUID, doi string) (bool, error)
	// ReplaceOlderDocument makes newestID the surviving document for a paper
	// previously represented by olderID. It repoints incoming citations and
	// collection membership to newest, transfers the DOI, drops the older
	// document's outgoing bibliography, and deletes older plus its merged
	// tombstones in one transaction. Stored objects from the removed family are
	// returned for post-commit cleanup.
	ReplaceOlderDocument(ctx context.Context, olderID, newestID uuid.UUID) (types.DocumentFileKeys, error)
	// GetStage returns one stage's durable control-plane snapshot.
	GetStage(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, error)
	// QueueStageNewGeneration increments a non-active stage generation and queues it atomically.
	QueueStageNewGeneration(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, bool, error)
	// TryStartStage moves queued to running for the exact generation.
	TryStartStage(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error)
	// MarkStageRetryQueued moves running back to queued without changing generation.
	MarkStageRetryQueued(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error)
	// FinishStage writes succeeded, failed, or skipped using a generation fence.
	FinishStage(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64, status types.DocumentStageStatus, errorCode, errorMessage string) (types.DocumentStageState, bool, error)
	// CancelStage moves queued or running to cancelled using a generation fence.
	CancelStage(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error)
	// AllowsStageWork is true only while the exact generation is running.
	AllowsStageWork(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64) (bool, error)
}

// DocumentService contains document lifecycle and storage orchestration.
type DocumentService interface {
	// CreateDocumentFromFile stores an upload and creates its source record.
	CreateDocumentFromFile(ctx context.Context, file *multipart.FileHeader, doi string) (*types.Document, error)
	// CreateDocumentFromURL resolves and safely downloads one explicit remote
	// PDF, creates its source record, and starts the normal document pipeline.
	CreateDocumentFromURL(ctx context.Context, command types.DocumentURLImportCommand) (*types.Document, error)
	// CreateDocumentsFromFiles independently accepts a bounded multipart batch.
	// Duplicate file hashes return the existing document instead of creating a
	// second source record; a failed item does not stop later items.
	CreateDocumentsFromFiles(ctx context.Context, files []*multipart.FileHeader, dois []string) (types.DocumentBatchResult, error)
	// CreateDocumentsFromURLs independently resolves and accepts a bounded URL
	// batch through the same SSRF-protected single-item import path.
	CreateDocumentsFromURLs(ctx context.Context, commands []types.DocumentURLImportCommand) (types.DocumentBatchResult, error)
	// GetDocumentByID parses and retrieves a document by its textual UUID.
	GetDocumentByID(ctx context.Context, id string) (*types.Document, error)
	GetReferenceProgress(ctx context.Context, id string) (types.PaperReferenceProgress, error)
	// UpdateDocumentInfo updates original-file and DOI fields. Nil fields keep
	// their stored value unchanged.
	UpdateDocumentInfo(ctx context.Context, id string, originalFilename, doi *string) (*types.Document, error)
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
	// RequestReferencePDFDownload queues a user-selected bibliography target;
	// reference parsing never downloads targets automatically.
	RequestReferencePDFDownload(ctx context.Context, id string) error

	// RecognizeDocument performs only the asynchronous recognition stage.
	RecognizeDocument(ctx context.Context, task *asynq.Task) error
	// ImportRemoteDocument executes an explicit reference-download task.
	ImportRemoteDocument(ctx context.Context, task *asynq.Task) error
}
