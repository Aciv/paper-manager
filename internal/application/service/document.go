package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type documentService struct {
	repo          interfaces.DocumentRepository
	storage       interfaces.FileStorage
	taskInspector interfaces.TaskInspector
	taskEnqueuer  interfaces.TaskEnqueuer
	recognizer    interfaces.DocumentRecognizer
	spans         SpanTracker
	// recognitionOutputDir is the trusted shared-volume root for directory
	// recognizers. It is deliberately separate from upload storage: recognizer
	// output is untrusted until ProcessDocument validates and copies it.
	recognitionOutputDir     string
	maxRecognitionOutputSize int64
	extractionTaskTimeout    time.Duration
	extractionTaskMaxRetry   int
}

// Upload conflict sentinels are aliases so handlers do not depend on storage
// or repository implementation details.
var (
	ErrDuplicateDOI      = types.ErrDuplicateDOI
	ErrDuplicateFileHash = types.ErrDuplicateFileHash
)

var (
	ErrInvalidDocumentFilename = errors.New("invalid document filename")
	ErrInvalidDocumentDOI      = errors.New("invalid document DOI")
	ErrDocumentParseFinished   = errors.New("document parsing has already finished")
	ErrDocumentDeleting        = errors.New("document is being deleted")
)

const maxDocumentDOILength = 255

// NewDocumentService wires document persistence and file storage. Keeping this
// orchestration here makes handlers independent from storage and database code.
func NewDocumentService(repo interfaces.DocumentRepository, storage interfaces.FileStorage) interfaces.DocumentService {
	return NewDocumentServiceWithTaskInspector(repo, storage, nil)
}

// NewDocumentServiceWithTaskInspector injects the queue inspector used by
// CancelDocumentParse. A nil inspector is valid for lightweight deployments.
func NewDocumentServiceWithTaskInspector(
	repo interfaces.DocumentRepository, storage interfaces.FileStorage, taskInspector interfaces.TaskInspector,
) interfaces.DocumentService {
	return &documentService{repo: repo, storage: storage, taskInspector: taskInspector}
}

// NewDocumentServiceWithProcessing is the production constructor for source
// document storage, recognition, queueing, and cancellation dependencies.
func NewDocumentServiceWithProcessing(
	cfg config.Config,
	repo interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	taskInspector interfaces.TaskInspector,
	taskEnqueuer interfaces.TaskEnqueuer,
	recognizer interfaces.DocumentRecognizer,
) interfaces.DocumentService {
	return newDocumentServiceWithProcessing(cfg, repo, storage, taskInspector, taskEnqueuer, recognizer, nil)
}

// NewDocumentServiceWithProcessingAndSpanTracker is the DI constructor used
// in production. The legacy constructor remains for focused service tests.
func NewDocumentServiceWithProcessingAndSpanTracker(
	cfg config.Config,
	repo interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	taskInspector interfaces.TaskInspector,
	taskEnqueuer interfaces.TaskEnqueuer,
	recognizer interfaces.DocumentRecognizer,
	spans SpanTracker,
) interfaces.DocumentService {
	return newDocumentServiceWithProcessing(cfg, repo, storage, taskInspector, taskEnqueuer, recognizer, spans)
}

func newDocumentServiceWithProcessing(
	cfg config.Config, repo interfaces.DocumentRepository, storage interfaces.FileStorage,
	taskInspector interfaces.TaskInspector, taskEnqueuer interfaces.TaskEnqueuer,
	recognizer interfaces.DocumentRecognizer, spans SpanTracker,
) interfaces.DocumentService {
	return &documentService{
		repo: repo, storage: storage, taskInspector: taskInspector, taskEnqueuer: taskEnqueuer,
		recognizer: recognizer, spans: spans, recognitionOutputDir: cfg.RecognitionOutputDirectory,
		maxRecognitionOutputSize: cfg.MaxUploadSize,
		extractionTaskTimeout:    cfg.PaperExtractionTaskTimeout,
		extractionTaskMaxRetry:   cfg.PaperExtractionTaskMaxRetry,
	}
}

func (s *documentService) CreateDocumentFromFile(ctx context.Context, file *multipart.FileHeader, doi string) (*types.Document, error) {
	if file == nil {
		return nil, errors.New("file is required")
	}
	doi = strings.ToLower(strings.TrimSpace(doi))
	if doi != "" {
		page, searchErr := s.repo.Search(ctx, types.DocumentSearch{DOI: doi, Page: 1, PageSize: 1})
		if searchErr != nil {
			logger.Error(ctx, "check document DOI failed", "doi", doi, "error", searchErr)
			return nil, searchErr
		}
		if page.Total > 0 {
			logger.Warn(ctx, "duplicate document DOI rejected", "doi", doi)
			return nil, ErrDuplicateDOI
		}
	}
	fileHash, err := multipartFileSHA256(file)
	if err != nil {
		return nil, err
	}
	page, searchErr := s.repo.Search(ctx, types.DocumentSearch{FileSHA256: fileHash, Page: 1, PageSize: 1})
	if searchErr != nil {
		logger.Error(ctx, "check document file hash failed", "error", searchErr)
		return nil, searchErr
	}
	if page.Total > 0 {
		logger.Warn(ctx, "duplicate document file hash rejected", "file_sha256", fileHash)
		return nil, types.ErrDuplicateFileHash
	}
	stored, err := fileservice.SaveMultipart(ctx, s.storage, file)
	if err != nil {
		logger.Error(ctx, "store document file failed", "filename", file.Filename, "error", err)
		return nil, err
	}
	if stored.SHA256 != "" && !strings.EqualFold(stored.SHA256, fileHash) {
		logger.Error(ctx, "stored document hash differs from uploaded bytes",
			"storage_key", stored.Key,
			"uploaded_file_sha256", fileHash,
			"stored_file_sha256", stored.SHA256)
		if cleanupErr := s.storage.Delete(context.WithoutCancel(ctx), stored.Key); cleanupErr != nil {
			logger.Warn(ctx, "remove hash-mismatched stored file failed", "storage_key", stored.Key, "error", cleanupErr)
		}
		return nil, errors.New("stored file hash differs from uploaded file hash")
	}
	doc := types.Document{ID: uuid.New(), OriginalFilename: stored.Filename, ContentType: stored.ContentType,
		SizeBytes: stored.Size, DOI: doi, FileSHA256: fileHash, StorageKey: stored.Key, Status: types.DocumentStatusUploaded}
	created, err := s.repo.Create(ctx, doc)
	if err != nil {
		logger.Error(ctx, "create document record failed", "document_id", doc.ID, "error", err)
		if cleanupErr := s.storage.Delete(context.WithoutCancel(ctx), stored.Key); cleanupErr != nil {
			logger.Warn(ctx, "remove orphaned document file failed", "storage_key", stored.Key, "error", cleanupErr)
		}
		return nil, err
	}
	logger.Info(ctx, "document created", "document_id", created.ID)
	if s.taskEnqueuer != nil {
		if err := s.enqueueDocumentRecognition(ctx, created.ID); err != nil {
			return nil, err
		}
	}
	return &created, nil
}

func multipartFileSHA256(file *multipart.FileHeader) (string, error) {
	input, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("open uploaded file for hash: %w", err)
	}
	defer input.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return "", fmt.Errorf("hash uploaded file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *documentService) enqueueDocumentRecognition(ctx context.Context, documentID uuid.UUID) error {
	transitioned, err := s.repo.UpdateStatusIfCurrent(ctx, documentID, types.DocumentStatusQueued, nil,
		[]types.DocumentStatus{types.DocumentStatusUploaded, types.DocumentStatusFailed, types.DocumentStatusCancelled, types.DocumentStatusReady})
	if err != nil {
		return fmt.Errorf("mark document queued: %w", err)
	}
	if !transitioned {
		return fmt.Errorf("document cannot be queued from its current status")
	}
	task, options, err := NewDocumentProcessTask(types.DocumentProcessPayload{
		DocumentID: documentID.String(),
	})
	if err != nil {
		return err
	}
	if err := enqueueManagedTask(
		ctx,
		s.taskEnqueuer,
		task,
		options,
		documentID.String(),
	); err != nil {
		message := "document recognition could not be queued: " + err.Error()
		if updateErr := s.repo.UpdateStatus(context.WithoutCancel(ctx), documentID, types.DocumentStatusFailed, &message); updateErr != nil {
			logger.Error(ctx, "record document queue failure failed", "document_id", documentID, "error", updateErr)
		}
		return err
	}
	return nil
}

// enqueueManagedTask submits a task built by an application service. A
// duplicate unique-window submission is idempotent, but a TaskID conflict is
// reported: an archived dead-letter task can own that ID without live work.
func enqueueManagedTask(
	ctx context.Context,
	enqueuer interfaces.TaskEnqueuer,
	task *asynq.Task,
	options []asynq.Option,
	documentID string,
) error {
	if enqueuer == nil {
		return errors.New("task queue is not configured")
	}
	if task == nil {
		return errors.New("task is required")
	}
	_, err := enqueuer.EnqueueContext(ctx, task, options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		logger.Debug(ctx, "task already queued",
			"document_id", documentID,
			"task_type", task.Type())
		return nil
	}
	if err != nil {
		logger.Error(ctx, "enqueue task failed",
			"document_id", documentID,
			"task_type", task.Type(),
			"error", err)
		return fmt.Errorf("enqueue %s: %w", task.Type(), err)
	}
	logger.Info(ctx, "task enqueued",
		"document_id", documentID,
		"task_type", task.Type())
	return nil
}

func (s *documentService) GetDocumentByID(ctx context.Context, id string) (*types.Document, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("invalid document id: %w", err)
	}
	doc, err := s.repo.Get(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (s *documentService) GetDocumentStatus(ctx context.Context, id string) (types.DocumentStatus, error) {
	doc, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return "", err
	}
	return doc.Status, nil
}

func (s *documentService) SetDocumentStatus(ctx context.Context, id string, status types.DocumentStatus, message *string) error {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("invalid document id: %w", err)
	}
	if !isDocumentStatus(status) {
		return fmt.Errorf("invalid document status %q", status)
	}
	if err := s.repo.UpdateStatus(ctx, parsed, status, message); err != nil {
		logger.Error(ctx, "set document status failed", "document_id", id, "status", status, "error", err)
		return err
	}
	return nil
}

func (s *documentService) UpdateDocumentInfo(
	ctx context.Context, id string, originalFilename, doi *string,
) (*types.Document, error) {
	if originalFilename == nil && doi == nil {
		return nil, errors.New("at least one document field is required")
	}
	document, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if originalFilename != nil {
		candidate := strings.TrimSpace(*originalFilename)
		safeName, safeErr := utils.SafeFileName(candidate)
		if safeErr != nil || safeName != candidate {
			return nil, ErrInvalidDocumentFilename
		}
		originalFilename = &safeName
	}
	if doi != nil {
		candidate := strings.TrimSpace(*doi)
		if !utf8.ValidString(candidate) || utf8.RuneCountInString(candidate) > maxDocumentDOILength || strings.ContainsAny(candidate, "\r\n\t") {
			return nil, ErrInvalidDocumentDOI
		}
		normalized := strings.ToLower(candidate)
		doi = &normalized
	}
	updated, err := s.repo.UpdateDocumentInfo(ctx, document.ID, originalFilename, doi)
	if err != nil {
		logger.Error(ctx, "update document info failed", "document_id", document.ID, "error", err)
		return nil, err
	}
	return &updated, nil
}

func (s *documentService) FailDocumentIfProcessing(ctx context.Context, id string, message string) (bool, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return false, fmt.Errorf("invalid document id: %w", err)
	}
	return s.repo.UpdateStatusIfCurrent(ctx, parsed, types.DocumentStatusFailed, &message,
		[]types.DocumentStatus{types.DocumentStatusQueued, types.DocumentStatusProcessing})
}

func (s *documentService) SearchDocument(ctx context.Context, search types.DocumentSearch) (types.DocumentPage, error) {
	search.DOI = strings.ToLower(strings.TrimSpace(search.DOI))
	return s.repo.Search(ctx, search)
}

func (s *documentService) GetDocumentFile(ctx context.Context, id string) (io.ReadCloser, string, error) {
	doc, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if doc.Status == types.DocumentStatusUnknown {
		return nil, "", sql.ErrNoRows
	}
	rc, err := s.storage.Open(ctx, doc.StorageKey)
	if err != nil {
		logger.Error(ctx, "open document file failed", "document_id", doc.ID, "error", err)
		return nil, "", err
	}
	return rc, doc.OriginalFilename, nil
}

func (s *documentService) GetDocumentBlocks(ctx context.Context, id string, pageNumber int) ([]types.Block, error) {
	if pageNumber < 0 {
		return nil, errors.New("page number must not be negative")
	}
	documentID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("invalid document id: %w", err)
	}
	if _, err := s.repo.Get(ctx, documentID); err != nil {
		return nil, err
	}
	return s.repo.ListBlocks(ctx, documentID, pageNumber)
}

func (s *documentService) GetDocumentBlockImage(ctx context.Context, documentID, blockID string) (io.ReadCloser, string, error) {
	documentUUID, err := uuid.Parse(strings.TrimSpace(documentID))
	if err != nil {
		return nil, "", fmt.Errorf("invalid document id: %w", err)
	}
	blockUUID, err := uuid.Parse(strings.TrimSpace(blockID))
	if err != nil {
		return nil, "", fmt.Errorf("invalid block id: %w", err)
	}
	block, err := s.repo.GetBlock(ctx, documentUUID, blockUUID)
	if err != nil {
		return nil, "", err
	}
	if block.AssetKey == "" {
		return nil, "", sql.ErrNoRows
	}
	reader, err := s.storage.Open(ctx, block.AssetKey)
	if err != nil {
		logger.Error(ctx, "open document block image failed", "document_id", documentUUID, "block_id", blockUUID, "storage_key", block.AssetKey, "error", err)
		return nil, "", err
	}
	return reader, filepath.Base(block.AssetKey), nil
}

func (s *documentService) DeleteDocument(ctx context.Context, id string) error {
	doc, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return err
	}
	transitioned, err := s.repo.UpdateStatusIfCurrent(ctx, doc.ID, types.DocumentStatusDeleting, nil,
		[]types.DocumentStatus{types.DocumentStatusUnknown, types.DocumentStatusUploaded, types.DocumentStatusQueued, types.DocumentStatusProcessing, types.DocumentStatusCancelled, types.DocumentStatusReady, types.DocumentStatusFailed})
	if err != nil {
		return err
	}
	if !transitioned && doc.Status != types.DocumentStatusDeleting {
		return fmt.Errorf("document cannot be deleted while status is %q", doc.Status)
	}

	markdown, err := s.repo.GetMarkdown(ctx, doc.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.restoreDocumentStatusAfterDeleteFailure(ctx, doc)
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		markdown = types.DocumentMarkdown{}
	}
	blocks, err := s.repo.ListBlocks(ctx, doc.ID, 0)
	if err != nil {
		s.restoreDocumentStatusAfterDeleteFailure(ctx, doc)
		return err
	}

	return s.deleteDocumentRelations(ctx, doc, &markdown, blocks)
}

// deleteDocumentRelations performs the non-database portion of document
// deletion. The repository then deletes the documents row, whose cascading
// foreign keys remove blocks, Markdown metadata, paper metadata, and base
// membership rows atomically.
func (s *documentService) deleteDocumentRelations(
	ctx context.Context, document *types.Document, markdown *types.DocumentMarkdown,
	blocks []types.Block,
) error {
	if document.Status != types.DocumentStatusUnknown {
		if s.storage == nil {
			return errors.New("file storage is not configured")
		}
		if err := s.storage.Delete(ctx, document.StorageKey); err != nil {
			s.restoreDocumentStatusAfterDeleteFailure(ctx, document)
			return err
		}
		logger.Info(ctx, "document source file deleted", "document_id", document.ID, "storage_key", document.StorageKey)
	}

	if markdown != nil && markdown.StorageKey != "" && markdown.StorageKey != document.StorageKey {
		if s.storage == nil {
			return errors.New("file storage is not configured")
		}
		if err := s.storage.Delete(ctx, markdown.StorageKey); err != nil {
			s.recordIncompleteDocumentDeletion(ctx, document, "document source file deleted; markdown deletion must be retried", err)
			return err
		}
		logger.Info(ctx, "document markdown file deleted", "document_id", document.ID, "storage_key", markdown.StorageKey)
	}
	for _, block := range blocks {
		if block.AssetKey == "" {
			continue
		}
		if s.storage == nil {
			return errors.New("file storage is not configured")
		}
		if err := s.storage.Delete(ctx, block.AssetKey); err != nil {
			s.recordIncompleteDocumentDeletion(ctx, document, "document source and Markdown files deleted; block image deletion must be retried", err)
			return err
		}
		logger.Info(ctx, "document block image deleted", "document_id", document.ID, "block_id", block.ID, "storage_key", block.AssetKey)
	}

	// Storage cleanup has succeeded and the row will remain deleting even if
	// the final database delete fails. It is now safe to purge recognition,
	// extraction, and catalog work without risking a restored document that
	// has silently lost its processing task.
	s.dequeueDocumentTasks(ctx, document.ID.String())
	if err := s.repo.Delete(ctx, document.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.recordIncompleteDocumentDeletion(ctx, document, "stored files deleted; database deletion must be retried", err)
		return err
	}
	logger.Info(ctx, "document deleted", "document_id", document.ID)
	return nil
}

func (s *documentService) restoreDocumentStatusAfterDeleteFailure(ctx context.Context, document *types.Document) {
	if restoreErr := s.repo.UpdateStatus(context.WithoutCancel(ctx), document.ID, document.Status, document.ErrorMessage); restoreErr != nil {
		logger.Warn(ctx, "restore document status after storage delete failure", "document_id", document.ID, "error", restoreErr)
	}
}

func (s *documentService) recordIncompleteDocumentDeletion(
	ctx context.Context, document *types.Document, prefix string, cause error,
) {
	message := prefix + ": " + cause.Error()
	if updateErr := s.repo.UpdateStatus(context.WithoutCancel(ctx), document.ID, types.DocumentStatusDeleting, &message); updateErr != nil {
		logger.Error(ctx, "record document deletion recovery state failed", "document_id", document.ID, "error", updateErr)
	}
}

func (s *documentService) DeleteDocumentList(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := s.DeleteDocument(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func isDocumentStatus(status types.DocumentStatus) bool {
	switch status {
	case types.DocumentStatusUploaded, types.DocumentStatusQueued, types.DocumentStatusProcessing,
		types.DocumentStatusCancelled, types.DocumentStatusDeleting, types.DocumentStatusReady, types.DocumentStatusFailed:
		return true
	default:
		return false
	}
}

// Compile-time checks for accidental interface drift.
var _ interfaces.DocumentService = (*documentService)(nil)
