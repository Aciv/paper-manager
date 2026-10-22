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
	"os"
	"path/filepath"
	"strings"
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
	paperInfo     interfaces.PaperInfoService
	spans         interfaces.SpanRecorder
	processing    interfaces.ProcessingViewQuery
	papers        interfaces.PaperService
	urlResolver   interfaces.PaperURLResolver
	downloads     interfaces.RemoteDownloadLimiter
	readingNotes  interfaces.NoteReadingRepository
	// recognitionOutputDir is the trusted shared-volume root for directory
	// recognizers. It is deliberately separate from upload storage: recognizer
	// output is untrusted until ProcessDocument validates and copies it.
	recognitionOutputDir      string
	maxRecognitionOutputSize  int64
	maxUploadSize             int64
	documentStructureCacheDir string
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
	ErrDocumentParseActive     = errors.New("document parsing is still active")
	ErrDocumentDeleting        = errors.New("document is being deleted")
	errDocumentDuplicateLookup = errors.New("document duplicate lookup failed")
	// ErrDocumentProcessQueue means the source file and document row were
	// committed, but the initial asynchronous process task was not accepted.
	// Callers receive the persisted document alongside this error and can use
	// the normal retry endpoint instead of asking users to upload again.
	ErrDocumentProcessQueue = errors.New("document process could not be queued")
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
	return &documentService{
		repo: repo, storage: storage, taskInspector: taskInspector,
		maxUploadSize: fileservice.DefaultMaxUploadSize,
	}
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
	return newDocumentServiceWithProcessing(cfg, repo, storage, taskInspector, taskEnqueuer, recognizer, nil, nil)
}

// NewDocumentServiceWithProcessingAndSpanRecorder is the DI constructor used
// in production. The legacy constructor remains for focused service tests.
func NewDocumentServiceWithProcessingAndSpanRecorder(
	cfg config.Config,
	repo interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	taskInspector interfaces.TaskInspector,
	taskEnqueuer interfaces.TaskEnqueuer,
	recognizer interfaces.DocumentRecognizer,
	paperInfo interfaces.PaperInfoService,
	spans interfaces.SpanRecorder,
	processing interfaces.ProcessingViewQuery,
	papers interfaces.PaperService,
	urlResolver interfaces.PaperURLResolver,
	downloads interfaces.RemoteDownloadLimiter,
	readingNotes interfaces.NoteReadingRepository,
) interfaces.DocumentService {
	service := newDocumentServiceWithProcessing(cfg, repo, storage, taskInspector, taskEnqueuer, recognizer, paperInfo, spans).(*documentService)
	service.processing = processing
	service.papers = papers
	service.urlResolver = urlResolver
	service.downloads = downloads
	service.readingNotes = readingNotes
	return service
}

func newDocumentServiceWithProcessing(
	cfg config.Config, repo interfaces.DocumentRepository, storage interfaces.FileStorage,
	taskInspector interfaces.TaskInspector, taskEnqueuer interfaces.TaskEnqueuer,
	recognizer interfaces.DocumentRecognizer, paperInfo interfaces.PaperInfoService, spans interfaces.SpanRecorder,
) interfaces.DocumentService {
	maxUploadSize := cfg.MaxUploadSize
	if maxUploadSize <= 0 {
		maxUploadSize = fileservice.DefaultMaxUploadSize
	}
	return &documentService{
		repo: repo, storage: storage, taskInspector: taskInspector, taskEnqueuer: taskEnqueuer,
		recognizer: recognizer, paperInfo: paperInfo, spans: spans, recognitionOutputDir: cfg.RecognitionOutputDirectory,
		maxRecognitionOutputSize: cfg.MaxUploadSize, maxUploadSize: maxUploadSize,
		documentStructureCacheDir: documentStructureCacheDirectory(cfg),
	}
}

// documentUploadSource is the common input for HTTP multipart uploads and
// explicitly authorized local-file imports. Open must return a fresh reader on
// every call so hashing and storage consume the same bytes independently.
type documentUploadSource struct {
	Filename    string
	Size        int64
	ContentType string
	Open        func() (io.ReadCloser, error)
}

func (s *documentService) CreateDocumentFromFile(ctx context.Context, file *multipart.FileHeader, doi string) (*types.Document, error) {
	if file == nil {
		return nil, errors.New("file is required")
	}
	return s.createDocumentFromSource(ctx, documentUploadSource{
		Filename: file.Filename, Size: file.Size,
		ContentType: file.Header.Get("Content-Type"),
		Open: func() (io.ReadCloser, error) {
			return file.Open()
		},
	}, doi)
}

// CreateDocumentFromLocalFile imports a regular file selected by a trusted
// caller. Path containment and symlink checks belong to that caller; the
// service only rejects non-regular files and applies the normal upload rules.
func (s *documentService) CreateDocumentFromLocalFile(ctx context.Context, path, doi string) (*types.Document, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("local file path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat local file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("local file must be a regular file")
	}
	return s.createDocumentFromSource(ctx, documentUploadSource{
		Filename: filepath.Base(path), Size: info.Size(),
		Open: func() (io.ReadCloser, error) {
			return os.Open(path)
		},
	}, doi)
}

func (s *documentService) createDocumentFromSource(
	ctx context.Context,
	source documentUploadSource,
	doi string,
) (*types.Document, error) {
	if source.Open == nil {
		return nil, errors.New("file source is required")
	}
	// Reject cheaply before the full SHA-256 pass. The handler performs the
	// same checks for an early 413/415; this keeps batch and future callers
	// from hashing an invalid file.
	if _, supported := fileservice.SupportedContentType(source.Filename); !supported {
		return nil, fileservice.ErrUnsupportedFileType
	}
	if source.Size < 0 || (s.maxUploadSize > 0 && source.Size > s.maxUploadSize) {
		return nil, fileservice.ErrFileTooLarge
	}
	doi = strings.ToLower(strings.TrimSpace(doi))
	if doi != "" {
		page, searchErr := s.repo.Search(ctx, types.DocumentSearch{DOI: doi, Page: 1, PageSize: 1})
		if searchErr != nil {
			logger.Error(ctx, "check document DOI failed", "doi", doi, "error", searchErr)
			return nil, fmt.Errorf("%w: %w", errDocumentDuplicateLookup, searchErr)
		}
		if page.Total > 0 {
			logger.Warn(ctx, "duplicate document DOI rejected", "doi", doi)
			return &page.Items[0], ErrDuplicateDOI
		}
	}
	fileHash, err := sourceSHA256(source)
	if err != nil {
		return nil, err
	}
	page, searchErr := s.repo.Search(ctx, types.DocumentSearch{FileSHA256: fileHash, Page: 1, PageSize: 1})
	if searchErr != nil {
		logger.Error(ctx, "check document file hash failed", "error", searchErr)
		return nil, fmt.Errorf("%w: %w", errDocumentDuplicateLookup, searchErr)
	}
	if page.Total > 0 {
		logger.Warn(ctx, "duplicate document file hash rejected", "file_sha256", fileHash)
		return &page.Items[0], types.ErrDuplicateFileHash
	}
	input, err := source.Open()
	if err != nil {
		return nil, fmt.Errorf("open uploaded file: %w", err)
	}
	category := interfaces.StorageCategory("")
	if strings.EqualFold(filepath.Ext(source.Filename), ".pdf") {
		category = interfaces.StorageCategoryPDF
	}
	stored, err := s.storage.Save(ctx, interfaces.FileUpload{
		Filename: source.Filename, ContentType: source.ContentType,
		Content: input, Category: category,
	})
	closeErr := input.Close()
	if err != nil {
		logger.Error(ctx, "store document file failed", "filename", source.Filename, "error", err)
		return nil, err
	}
	if closeErr != nil {
		logger.Warn(ctx, "close uploaded file failed", "filename", source.Filename, "error", closeErr)
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
		SizeBytes: stored.Size, DOI: doi, FileSHA256: fileHash, StorageKey: stored.Key,
		SourceState: types.DocumentSourceAvailable}
	created, err := s.repo.Create(ctx, doc)
	if err != nil {
		logger.Error(ctx, "create document record failed", "document_id", doc.ID, "error", err)
		if cleanupErr := s.storage.Delete(context.WithoutCancel(ctx), stored.Key); cleanupErr != nil {
			logger.Warn(ctx, "remove orphaned document file failed", "storage_key", stored.Key, "error", cleanupErr)
		}
		if errors.Is(err, types.ErrDuplicateFileHash) {
			if duplicate, lookupErr := s.existingDocument(ctx, "", fileHash); lookupErr == nil && duplicate != nil {
				return duplicate, err
			}
		}
		if errors.Is(err, types.ErrDuplicateDOI) {
			if duplicate, lookupErr := s.existingDocument(ctx, doi, ""); lookupErr == nil && duplicate != nil {
				return duplicate, err
			}
		}
		return nil, err
	}
	logger.Info(ctx, "document created", "document_id", created.ID)
	return s.startCreatedDocumentProcessing(ctx, created)
}

// startCreatedDocumentProcessing starts the initial parse after the file and
// row are durable. A queue outage must not hide that successful creation:
// callers get the refreshed failed document plus ErrDocumentProcessQueue and
// can invoke RetryDocumentParse without uploading the same file again.
func (s *documentService) startCreatedDocumentProcessing(
	ctx context.Context,
	created types.Document,
) (*types.Document, error) {
	if s.taskEnqueuer == nil {
		return &created, nil
	}
	if err := s.enqueueDocumentRecognition(ctx, created.ID); err != nil {
		persisted := s.loadCreatedDocumentAfterQueue(ctx, created)
		logger.Warn(ctx, "document created but initial process was not queued",
			"document_id", persisted.ID,
			"markdown_status", persisted.MarkdownStatus,
			"error", err)
		return &persisted, fmt.Errorf("%w: %w", ErrDocumentProcessQueue, err)
	}
	returnDocument := s.loadCreatedDocumentAfterQueue(ctx, created)
	return &returnDocument, nil
}

// loadCreatedDocumentAfterQueue returns the database state after a queue
// transition. Repository read failure is non-fatal here because the caller
// already holds the durable created row; logging preserves the diagnostic.
func (s *documentService) loadCreatedDocumentAfterQueue(
	ctx context.Context,
	created types.Document,
) types.Document {
	persisted, err := s.repo.Get(ctx, created.ID)
	if err != nil {
		logger.Warn(ctx, "reload created document after queue transition failed",
			"document_id", created.ID, "error", err)
		return created
	}
	return persisted
}

func sourceSHA256(source documentUploadSource) (string, error) {
	input, err := source.Open()
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

// existingDocument reloads the canonical source document after a duplicate
// DOI/hash result. A read failure means callers cannot safely report a
// duplicate, so it shares the duplicate-lookup error contract used by both
// file and URL creation paths.
func (s *documentService) existingDocument(ctx context.Context, doi, hash string) (*types.Document, error) {
	id, err := s.findDuplicateDocument(ctx, uuid.Nil, doi, hash)
	if err != nil || id == uuid.Nil {
		return nil, err
	}
	document, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDocumentDuplicateLookup, err)
	}
	return &document, nil
}

// enqueueDocumentRecognition creates one durable Markdown generation before
// publishing the dedicated recognition task.
func (s *documentService) enqueueDocumentRecognition(ctx context.Context, documentID uuid.UUID, _ ...int) error {
	stage, applied, err := s.repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil {
		return fmt.Errorf("queue Markdown stage: %w", err)
	}
	if !applied {
		return ErrDocumentParseActive
	}
	payload := types.DocumentProcessPayload{
		DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown,
		Generation: stage.Generation,
	}
	task, options, err := NewDocumentRecognitionTask(payload)
	if err == nil {
		err = enqueueManagedTask(ctx, s.taskEnqueuer, task, options, documentID.String())
	}
	if err != nil {
		_, _, _ = s.repo.FinishStage(context.WithoutCancel(ctx), documentID, types.DocumentStageMarkdown, stage.Generation, types.DocumentStageFailed, types.ProcessingErrorTaskFailed, "processing task could not be queued")
		return err
	}
	logger.Info(ctx, "document recognition queued", "document_id", documentID, "generation", stage.Generation)
	return nil
}

// RetryDocumentProcess retries the next failed/cancelled stage without using
// the legacy aggregate document status.
func (s *documentService) RetryDocumentProcess(ctx context.Context, id string) (*types.Document, error) {
	document, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return nil, ErrDocumentDeleting
	}
	markdownStage, err := s.repo.GetStage(ctx, document.ID, types.DocumentStageMarkdown)
	if err != nil {
		return nil, err
	}
	if markdownStage.Status.Active() {
		return nil, ErrDocumentParseActive
	}
	if markdownStage.Status == types.DocumentStageSucceeded {
		if s.paperInfo == nil {
			return nil, errors.New("paper information service is not configured")
		}
		if err := s.paperInfo.RetryExtraction(ctx, document.ID.String()); err != nil {
			return nil, err
		}
		logger.Info(ctx, "document metadata retry started", "document_id", document.ID)
		return document, nil
	}
	if markdownStage.Status != types.DocumentStageFailed && markdownStage.Status != types.DocumentStageCancelled && markdownStage.Status != types.DocumentStageSkipped {
		return nil, ErrDocumentParseFinished
	}
	if err := s.enqueueDocumentRecognition(ctx, document.ID); err != nil {
		failed := s.loadCreatedDocumentAfterQueue(ctx, *document)
		logger.Warn(ctx, "document process retry was not queued",
			"document_id", failed.ID, "markdown_status", failed.MarkdownStatus, "error", err)
		return &failed, fmt.Errorf("%w: %w", ErrDocumentProcessQueue, err)
	}
	logger.Info(ctx, "document process retry started", "document_id", document.ID)
	return document, nil
}

// RetryDocumentParse starts a distinct recognition attempt only after the
// previous parse ended. It intentionally does not touch paper-info work:
// those timelines are independent of source document parsing.
func (s *documentService) RetryDocumentParse(ctx context.Context, id string) (*types.Document, error) {
	document, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return nil, ErrDocumentDeleting
	}
	stage, err := s.repo.GetStage(ctx, document.ID, types.DocumentStageMarkdown)
	if err != nil {
		return nil, err
	}
	if stage.Status.Active() {
		return nil, ErrDocumentParseActive
	}
	if stage.Status != types.DocumentStageFailed && stage.Status != types.DocumentStageCancelled && stage.Status != types.DocumentStageSkipped {
		return nil, ErrDocumentParseFinished
	}
	if err := s.enqueueDocumentRecognition(ctx, document.ID); err != nil {
		failed := s.loadCreatedDocumentAfterQueue(ctx, *document)
		return &failed, fmt.Errorf("%w: %w", ErrDocumentProcessQueue, err)
	}
	queued, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "document recognition retry queued", "document_id", queued.ID, "generation", queued.MarkdownGeneration)
	return queued, nil
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

func (s *documentService) GetReferenceProgress(ctx context.Context, id string) (types.PaperReferenceProgress, error) {
	documentID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil || documentID == uuid.Nil {
		return types.PaperReferenceProgress{}, errors.New("invalid document id")
	}
	return s.repo.CitationTargetProgress(ctx, documentID)
}

// maxDocumentProjectionIDs bounds one batch read-model request. Callers page
// documents and then batch only the current page.
const maxDocumentProjectionIDs = 200

func (s *documentService) GetDocumentProjections(
	ctx context.Context, query types.DocumentProjectionQuery,
) ([]types.DocumentProjection, error) {
	ids := make([]uuid.UUID, 0, len(query.DocumentIDs))
	seen := make(map[uuid.UUID]struct{}, len(query.DocumentIDs))
	for _, raw := range query.DocumentIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil || id == uuid.Nil {
			return nil, errors.New("invalid document id")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []types.DocumentProjection{}, nil
	}
	if len(ids) > maxDocumentProjectionIDs {
		return nil, fmt.Errorf("at most %d document ids are allowed", maxDocumentProjectionIDs)
	}
	documents, err := s.repo.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	metadataByID := map[uuid.UUID]types.PaperMetadata{}
	if query.IncludeMetadata && s.papers != nil {
		if metadataByID, err = s.papers.GetMetadataBatch(ctx, ids); err != nil {
			return nil, err
		}
	}
	var views []types.DocumentProcessingView
	if query.IncludeProcessing {
		if s.processing != nil {
			if views, err = s.processing.GetViews(ctx, documents); err != nil {
				return nil, err
			}
		} else {
			views = make([]types.DocumentProcessingView, len(documents))
			for index, document := range documents {
				views[index] = basicProcessingView(document)
			}
		}
	}
	progressByID := map[uuid.UUID]types.PaperReferenceProgress{}
	if query.IncludeProcessing {
		if progressByID, err = s.repo.CitationTargetProgressBatch(ctx, ids); err != nil {
			// Progress is supplemental, matching the single-document endpoint.
			logger.Warn(ctx, "load batched reference progress failed", "document_count", len(ids), "error", err)
			progressByID = map[uuid.UUID]types.PaperReferenceProgress{}
		}
	}

	projections := make([]types.DocumentProjection, 0, len(documents))
	for index, document := range documents {
		projection := types.DocumentProjection{Document: document}
		if query.IncludeMetadata {
			metadata, found := metadataByID[document.ID]
			if !found {
				metadata = types.PaperMetadata{
					DocumentID: document.ID, Authors: []types.PaperAuthor{},
					Tags: []string{}, Metadata: types.JSON(`{}`),
					MetadataSource: types.PaperMetadataSourceUnknown,
				}
			}
			projection.Metadata = &metadata
		}
		if query.IncludeProcessing {
			view := views[index]
			progress := progressByID[document.ID]
			view.ReferenceProgress = &progress
			projection.Processing = &view
		}
		projections = append(projections, projection)
	}
	return projections, nil
}

func basicProcessingView(document types.Document) types.DocumentProcessingView {
	view := types.DocumentProcessingView{
		DocumentID: document.ID, SourceState: document.SourceState,
		CanonicalDocumentID: document.CanonicalDocumentID,
		StageStates:         []types.DocumentStageState{},
		Stages:              []types.DocumentProcessingSpan{},
	}
	for _, stage := range []types.DocumentStage{
		types.DocumentStageMarkdown, types.DocumentStageMetadataExtraction,
		types.DocumentStageSourceLookup, types.DocumentStageReferenceLookup,
	} {
		if state, err := document.StageState(stage); err == nil {
			view.StageStates = append(view.StageStates, state)
		}
	}
	return view
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

func (s *documentService) SearchDocument(ctx context.Context, search types.DocumentSearch) (types.DocumentPage, error) {
	search.DOI = strings.ToLower(strings.TrimSpace(search.DOI))
	return s.repo.Search(ctx, search)
}

func (s *documentService) DeleteDocument(ctx context.Context, id string) error {
	doc, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return err
	}
	// Stop active bibliography runs before the row goes "deleting" (the
	// paper-info cancel guard refuses that status). Best-effort: pending
	// in-memory dispatcher jobs are invalidated by the stage generation, and
	// already-dispatched results are generation-fenced.
	if s.paperInfo != nil {
		if cancelErr := s.paperInfo.Cancel(ctx, id); cancelErr != nil {
			logger.Warn(ctx, "cancel reference runs before document deletion failed", "document_id", doc.ID, "error", cancelErr)
		}
	}
	previousSourceState, transitioned, err := s.repo.MarkDeleting(ctx, doc.ID)
	if err != nil {
		return err
	}
	if !transitioned && doc.SourceState != types.DocumentSourceDeleting {
		return fmt.Errorf("document cannot be deleted while source state is %q", doc.SourceState)
	}

	markdown, err := s.repo.GetMarkdown(ctx, doc.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.restoreDocumentSourceStateAfterDeleteFailure(ctx, doc.ID, previousSourceState)
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		markdown = types.DocumentMarkdown{}
	}
	blocks, err := s.repo.ListBlocks(ctx, doc.ID, 0)
	if err != nil {
		s.restoreDocumentSourceStateAfterDeleteFailure(ctx, doc.ID, previousSourceState)
		return err
	}
	var readingNote types.DocumentReadingNote
	if s.readingNotes != nil {
		readingNote, err = s.readingNotes.Get(ctx, doc.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.restoreDocumentSourceStateAfterDeleteFailure(ctx, doc.ID, previousSourceState)
			return err
		}
	}

	return s.deleteDocumentRelations(ctx, doc, &markdown, blocks, &readingNote, previousSourceState)
}

// deleteDocumentRelations performs the non-database portion of document
// deletion. The repository then deletes the documents row, whose cascading
// foreign keys remove blocks, Markdown metadata, paper metadata, and base
// membership rows atomically.
func (s *documentService) deleteDocumentRelations(
	ctx context.Context, document *types.Document, markdown *types.DocumentMarkdown,
	blocks []types.Block, readingNote *types.DocumentReadingNote, previousSourceState types.DocumentSourceState,
) error {
	if s.storage == nil {
		return errors.New("file storage is not configured")
	}
	if err := s.storage.Delete(ctx, document.StorageKey); err != nil {
		s.restoreDocumentSourceStateAfterDeleteFailure(ctx, document.ID, previousSourceState)
		return err
	}
	logger.Info(ctx, "document source file deleted", "document_id", document.ID, "storage_key", document.StorageKey)

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
	if readingNote != nil {
		for _, section := range readingNote.Directory.Sections {
			if section.StorageKey == "" {
				continue
			}
			if err := s.storage.Delete(ctx, section.StorageKey); err != nil {
				s.recordIncompleteDocumentDeletion(ctx, document, "document files deleted; reading note deletion must be retried", err)
				return err
			}
			logger.Info(ctx, "document reading note section deleted", "document_id", document.ID, "storage_key", section.StorageKey)
		}
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
	if s.documentStructureCacheDir != "" {
		cachePath := filepath.Join(s.documentStructureCacheDir, document.ID.String()+".json")
		if removeErr := os.Remove(cachePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			logger.Warn(ctx, "remove deleted document structure cache failed", "document_id", document.ID, "error", removeErr)
		}
	}
	return nil
}

// dequeueDocumentTasks is reserved for document deletion, where every stage
// is fenced by source_state=deleting and broad queue cleanup is therefore safe.
func (s *documentService) dequeueDocumentTasks(ctx context.Context, documentID string) {
	if s.taskInspector == nil {
		return
	}
	if _, _, err := s.taskInspector.CancelTasksForDocument(context.WithoutCancel(ctx), documentID); err != nil {
		logger.Warn(ctx, "dequeue deleting document tasks failed", "document_id", documentID, "error", err)
	}
}

func (s *documentService) restoreDocumentSourceStateAfterDeleteFailure(ctx context.Context, documentID uuid.UUID, state types.DocumentSourceState) {
	if restoreErr := s.repo.RestoreSourceState(context.WithoutCancel(ctx), documentID, state); restoreErr != nil {
		logger.Warn(ctx, "restore document source state after storage delete failure", "document_id", documentID, "error", restoreErr)
	}
}

func (s *documentService) recordIncompleteDocumentDeletion(
	ctx context.Context, document *types.Document, prefix string, cause error,
) {
	logger.Error(ctx, "document deletion requires recovery", "document_id", document.ID, "reason", prefix, "error", cause)
}

func (s *documentService) DeleteDocumentList(ctx context.Context, ids []string) error {
	for _, id := range ids {
		if err := s.DeleteDocument(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Compile-time checks for accidental interface drift.
var _ interfaces.DocumentService = (*documentService)(nil)
