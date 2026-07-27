package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
	"unicode/utf8"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
)

type documentService struct {
	repo          interfaces.DocumentRepository
	papers        interfaces.PaperMetadataRepository
	storage       interfaces.FileStorage
	taskInspector interfaces.TaskInspector
	taskEnqueuer  interfaces.DocumentTaskEnqueuer
	recognizer    interfaces.DocumentRecognizer
	// recognitionOutputDir is the trusted shared-volume root for directory
	// recognizers. It is deliberately separate from upload storage: recognizer
	// output is untrusted until ProcessDocument validates and copies it.
	recognitionOutputDir     string
	maxRecognitionOutputSize int64
}

// ErrDuplicateDOI indicates that a document with the same DOI already exists.
var ErrDuplicateDOI = types.ErrDuplicateDOI

var (
	ErrInvalidDocumentFilename = errors.New("invalid document filename")
	ErrInvalidDocumentDOI      = errors.New("invalid document DOI")
	ErrInvalidPaperMetadata    = errors.New("invalid paper metadata")
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

// NewDocumentServiceWithPaperMetadata is the production constructor. It
// injects the separate bibliographic repository while keeping one document
// domain service as the handler-facing orchestration boundary.
func NewDocumentServiceWithPaperMetadata(
	cfg config.Config,
	repo interfaces.DocumentRepository,
	papers interfaces.PaperMetadataRepository,
	storage interfaces.FileStorage,
	taskInspector interfaces.TaskInspector,
	taskEnqueuer interfaces.DocumentTaskEnqueuer,
	recognizer interfaces.DocumentRecognizer,
) interfaces.DocumentService {
	return &documentService{
		repo: repo, papers: papers, storage: storage, taskInspector: taskInspector, taskEnqueuer: taskEnqueuer, recognizer: recognizer,
		recognitionOutputDir: cfg.RecognitionOutputDirectory, maxRecognitionOutputSize: cfg.MaxUploadSize,
	}
}

func (s *documentService) GetRepository() interfaces.DocumentRepository {
	return s.repo
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
	stored, err := fileservice.SaveMultipart(ctx, s.storage, file)
	if err != nil {
		logger.Error(ctx, "store document file failed", "filename", file.Filename, "error", err)
		return nil, err
	}
	doc := types.Document{ID: uuid.New(), OriginalFilename: stored.Filename, ContentType: stored.ContentType,
		SizeBytes: stored.Size, DOI: doi, StorageKey: stored.Key, Status: types.DocumentStatusUploaded}
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

func (s *documentService) enqueueDocumentRecognition(ctx context.Context, documentID uuid.UUID) error {
	transitioned, err := s.repo.UpdateStatusIfCurrent(ctx, documentID, types.DocumentStatusQueued, nil,
		[]types.DocumentStatus{types.DocumentStatusUploaded, types.DocumentStatusFailed, types.DocumentStatusCancelled, types.DocumentStatusReady})
	if err != nil {
		return fmt.Errorf("mark document queued: %w", err)
	}
	if !transitioned {
		return fmt.Errorf("document cannot be queued from its current status")
	}
	if err := s.taskEnqueuer.EnqueueDocumentProcess(ctx, types.DocumentProcessPayload{DocumentID: documentID.String()}); err != nil {
		message := "document recognition could not be queued: " + err.Error()
		if updateErr := s.repo.UpdateStatus(context.WithoutCancel(ctx), documentID, types.DocumentStatusFailed, &message); updateErr != nil {
			logger.Error(ctx, "record document queue failure failed", "document_id", documentID, "error", updateErr)
		}
		return err
	}
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

// GetPaperMetadata returns a virtual empty metadata value for a document that
// has not yet received bibliographic data. This lets a newly uploaded file be
// edited through the paper API without forcing an empty row at upload time.
func (s *documentService) GetPaperMetadata(ctx context.Context, documentID string) (*types.PaperMetadata, error) {
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	if s.papers == nil {
		return nil, errors.New("paper metadata repository is not configured")
	}
	metadata, err := s.papers.GetByDocumentID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return &types.PaperMetadata{DocumentID: id, Authors: []types.PaperAuthor{}, Tags: []string{}, Metadata: types.JSON(`{}`)}, nil
	}
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (s *documentService) UpdatePaperMetadata(ctx context.Context, documentID string, update types.PaperMetadataUpdate) (*types.PaperMetadata, error) {
	if !update.HasChanges() {
		return nil, errors.New("at least one paper metadata field is required")
	}
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	if s.papers == nil {
		return nil, errors.New("paper metadata repository is not configured")
	}
	normalized, err := normalizePaperMetadataUpdate(&update)
	if err != nil {
		return nil, err
	}
	updated, err := s.papers.Upsert(ctx, id, *normalized)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *documentService) SearchPaperMetadata(ctx context.Context, search types.PaperSearch) (types.PaperMetadataPage, error) {
	if s.papers == nil {
		return types.PaperMetadataPage{}, errors.New("paper metadata repository is not configured")
	}
	search.Title = strings.TrimSpace(search.Title)
	search.Author = strings.TrimSpace(search.Author)
	if search.TagMode == "" {
		search.TagMode = types.PaperTagModeAll
	}
	if search.TagMode != types.PaperTagModeAll && search.TagMode != types.PaperTagModeAny {
		return types.PaperMetadataPage{}, fmt.Errorf("invalid tag mode %q", search.TagMode)
	}
	tags, err := normalizePaperTags(search.Tags)
	if err != nil {
		return types.PaperMetadataPage{}, err
	}
	search.Tags = tags
	return s.papers.Search(ctx, search)
}

func (s *documentService) GetDocumentFile(ctx context.Context, id string) (io.ReadCloser, string, error) {
	doc, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, "", err
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

func (s *documentService) DeleteDocument(ctx context.Context, id string) error {
	doc, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return err
	}
	transitioned, err := s.repo.UpdateStatusIfCurrent(ctx, doc.ID, types.DocumentStatusDeleting, nil,
		[]types.DocumentStatus{types.DocumentStatusUploaded, types.DocumentStatusQueued, types.DocumentStatusProcessing, types.DocumentStatusCancelled, types.DocumentStatusReady, types.DocumentStatusFailed})
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

	return s.deleteDocumentRelations(ctx, doc, &markdown)
}

// deleteDocumentRelations performs the non-database portion of document
// deletion. The repository then deletes the documents row, whose cascading
// foreign keys remove blocks, Markdown metadata, paper metadata, and base
// membership rows atomically.
func (s *documentService) deleteDocumentRelations(
	ctx context.Context, document *types.Document, markdown *types.DocumentMarkdown,
) error {
	if s.storage == nil {
		return errors.New("file storage is not configured")
	}
	if err := s.storage.Delete(ctx, document.StorageKey); err != nil {
		s.restoreDocumentStatusAfterDeleteFailure(ctx, document)
		return err
	}
	logger.Info(ctx, "document source file deleted", "document_id", document.ID, "storage_key", document.StorageKey)

	if markdown != nil && markdown.StorageKey != "" && markdown.StorageKey != document.StorageKey {
		if err := s.storage.Delete(ctx, markdown.StorageKey); err != nil {
			s.recordIncompleteDocumentDeletion(ctx, document, "document source file deleted; markdown deletion must be retried", err)
			return err
		}
		logger.Info(ctx, "document markdown file deleted", "document_id", document.ID, "storage_key", markdown.StorageKey)
	}

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

func parsePaperDocumentID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid document id: %w", err)
	}
	return id, nil
}

func normalizePaperMetadataUpdate(update *types.PaperMetadataUpdate) (*types.PaperMetadataUpdate, error) {
	if update == nil {
		return nil, nil
	}
	normalized := *update
	for _, field := range []*string{normalized.Title, normalized.Abstract, normalized.SourceName, normalized.SourceURL, normalized.Venue, normalized.Volume, normalized.Issue, normalized.Pages, normalized.Language} {
		if field == nil {
			continue
		}
		if !utf8.ValidString(*field) || strings.ContainsAny(*field, "\r\n\t") {
			return nil, fmt.Errorf("%w: text contains invalid characters", ErrInvalidPaperMetadata)
		}
		*field = strings.TrimSpace(*field)
	}
	if normalized.Tags != nil {
		tags, err := normalizePaperTags(*normalized.Tags)
		if err != nil {
			return nil, err
		}
		normalized.Tags = &tags
	}
	if normalized.Authors != nil {
		authors := make([]types.PaperAuthor, 0, len(*normalized.Authors))
		for _, author := range *normalized.Authors {
			author.Name, author.Affiliation = strings.TrimSpace(author.Name), strings.TrimSpace(author.Affiliation)
			if author.Name == "" || !utf8.ValidString(author.Name) || strings.ContainsAny(author.Name, "\r\n\t") {
				return nil, fmt.Errorf("%w: authors must have a valid name", ErrInvalidPaperMetadata)
			}
			authors = append(authors, author)
		}
		normalized.Authors = &authors
	}
	return &normalized, nil
}

func normalizePaperTags(input []string) ([]string, error) {
	const maxPaperTagCount, maxPaperTagLength = 32, 64
	if len(input) > maxPaperTagCount {
		return nil, fmt.Errorf("%w: tags cannot contain more than %d values", ErrInvalidPaperMetadata, maxPaperTagCount)
	}
	seen, tags := make(map[string]struct{}, len(input)), make([]string, 0, len(input))
	for _, tag := range input {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if !utf8.ValidString(tag) || tag == "" || utf8.RuneCountInString(tag) > maxPaperTagLength || strings.ContainsAny(tag, "\r\n\t") {
			return nil, fmt.Errorf("%w: tags must be non-empty values up to 64 characters", ErrInvalidPaperMetadata)
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags, nil
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
