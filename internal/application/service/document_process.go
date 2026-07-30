package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	defaultDocumentProcessTaskTimeout  = 15 * time.Minute
	defaultDocumentProcessTaskMaxRetry = 3

	defaultPaperExtractionTaskTimeout  = 2 * time.Minute
	defaultPaperExtractionTaskMaxRetry = 3
	defaultPaperCatalogTaskTimeout     = 10 * time.Minute
	defaultPaperCatalogTaskMaxRetry    = 5
)

// ProcessDocument is the Asynq entry point for recognition. The parser's
// Markdown and source-map blocks are committed together, so readers never see
// a ready Markdown revision paired with stale PDF coordinates.
func (s *documentService) ProcessDocument(ctx context.Context, task *asynq.Task) (processErr error) {
	if task == nil {
		return errors.New("document processing task is required")
	}
	var payload types.DocumentProcessPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode document processing payload: %w", err)
	}
	documentID, err := uuid.Parse(strings.TrimSpace(payload.DocumentID))
	if err != nil {
		return fmt.Errorf("invalid document id in processing payload: %w", err)
	}
	document, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusCancelled || document.Status == types.DocumentStatusDeleting {
		logger.Info(ctx, "skip document recognition after cancellation or deletion", "document_id", document.ID, "status", document.Status)
		return nil
	}
	if document.Status == types.DocumentStatusReady {
		logger.Info(ctx, "document recognition already committed; ensuring paper info handoff", "document_id", document.ID)
		markdown, err := s.repo.GetMarkdown(ctx, document.ID)
		if err != nil {
			return fmt.Errorf("load committed Markdown for paper info handoff: %w", err)
		}
		return s.enqueuePaperInfoAfterRecognition(ctx, document, markdown.SHA256)
	}
	if document.Status != types.DocumentStatusProcessing {
		transitioned, err := s.repo.UpdateStatusIfCurrent(ctx, document.ID, types.DocumentStatusProcessing, nil,
			[]types.DocumentStatus{types.DocumentStatusUploaded, types.DocumentStatusQueued, types.DocumentStatusFailed})
		if err != nil {
			return fmt.Errorf("mark document processing: %w", err)
		}
		if !transitioned {
			logger.Info(ctx, "skip document recognition after concurrent status transition", "document_id", document.ID)
			return nil
		}
	}
	attempt := payload.Attempt
	if attempt < 1 {
		attempt = 1
	}
	retryCount, _ := asynq.GetRetryCount(ctx)
	var spanID uuid.UUID
	if s.spans != nil {
		var spanErr error
		spanID, spanErr = s.spans.Begin(ctx, document.ID, attempt, types.ProcessingSpanRecognition, types.ProcessingSpanScopeDocument, types.ComposeProcessingSpanScopeID(types.ProcessingSpanRecognition, document.ID.String()), retryCount)
		if spanErr != nil {
			logger.Warn(ctx, "start recognition processing span failed", "document_id", document.ID, "error", spanErr)
		}
	}
	defer func() {
		if s.spans == nil {
			return
		}
		if processErr != nil {
			code, reason := spanFailure(processErr)
			if err := s.spans.Fail(context.WithoutCancel(ctx), spanID, code, reason); err != nil {
				logger.Warn(ctx, "mark recognition processing span failed", "document_id", document.ID, "error", err)
			}
			return
		}
		if err := s.spans.Succeed(context.WithoutCancel(ctx), spanID); err != nil {
			logger.Warn(ctx, "finish recognition processing span failed", "document_id", document.ID, "error", err)
		}
	}()
	if s.storage == nil || s.recognizer == nil {
		return errors.New("document recognition dependencies are not configured")
	}

	logger.Info(ctx, "start document recognition", "document_id", document.ID, "content_type", document.ContentType)
	input, err := s.storage.Open(ctx, document.StorageKey)
	if err != nil {
		return fmt.Errorf("open document source: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(input, document.SizeBytes+1))
	closeErr := input.Close()
	if readErr != nil {
		return fmt.Errorf("read document source: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close document source: %w", closeErr)
	}
	if int64(len(content)) > document.SizeBytes {
		return fmt.Errorf("document source exceeds recorded size")
	}
	result, err := s.recognizer.Read(ctx, &types.RecognitionRequest{
		DocumentID:       document.ID,
		OriginalFilename: document.OriginalFilename,
		ContentType:      document.ContentType,
		SizeBytes:        int64(len(content)),
		InputMode:        types.RecognitionInputBytes,
		Content:          content,
	})

	if err != nil {
		if errors.Is(err, types.ErrUnsupportedRecognitionContentType) {
			return s.failUnsupportedDocumentRecognition(ctx, document.ID, err)
		}
		if errors.Is(err, types.ErrRecognitionServiceUnavailable) {
			return s.failUnavailableDocumentRecognition(ctx, document.ID, err)
		}
		return fmt.Errorf("recognize document: %w", err)
	}
	// Recognition can take minutes. Re-read the durable state before doing any
	// output work: a concurrent cancel or delete must discard this result rather
	// than creating a new stored Markdown object after the user ended the job.
	// ReplaceParsedOutput retains its processing-only condition as a second,
	// atomic guard for a status change that races this read.
	current, err := s.repo.Get(ctx, document.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logger.Info(ctx, "discard recognized output after document deletion", "document_id", document.ID)
			return nil
		}
		return fmt.Errorf("reload document after recognition: %w", err)
	}
	if current.Status != types.DocumentStatusProcessing {
		logger.Info(ctx, "discard recognized output after status change", "document_id", document.ID, "status", current.Status)
		return nil
	}
	markdownBytes, metadata, err := s.readRecognitionMarkdown(result)
	if err != nil {
		return fmt.Errorf("read recognizer Markdown output: %w", err)
	}

	previous, err := s.repo.GetMarkdown(ctx, document.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load previous Markdown: %w", err)
	}
	previousBlocks, err := s.repo.ListBlocks(ctx, document.ID, 0)
	if err != nil {
		return fmt.Errorf("load previous document blocks: %w", err)
	}
	blocks, imageKeys, err := s.persistBlocks(ctx, document.ID, metadata.SHA256, result.Blocks)
	if err != nil {
		return fmt.Errorf("store recognized block images: %w", err)
	}
	cleanupImages := func() {
		s.deleteStoredObjects(context.WithoutCancel(ctx), imageKeys, "remove uncommitted block image failed", document.ID)
	}
	storedMarkdown, err := s.storage.Save(ctx, interfaces.FileUpload{
		Filename: metadata.MarkdownFilename, ContentType: metadata.ContentType,
		Content: bytes.NewReader(markdownBytes), Category: interfaces.StorageCategoryMarkdown,
	})
	if err != nil {
		cleanupImages()
		return fmt.Errorf("store recognized Markdown: %w", err)
	}
	markdown := types.DocumentMarkdown{
		DocumentID: document.ID, StorageKey: storedMarkdown.Key, Filename: storedMarkdown.Filename,
		ContentType: metadata.ContentType, SizeBytes: storedMarkdown.Size, SHA256: metadata.SHA256,
		ParserName: metadata.ParserName, ParserVersion: metadata.ParserVersion,
		InputMode: string(types.RecognitionInputBytes), Language: metadata.Language,
		PageCount: metadata.PageCount, Metadata: types.JSON(`{}`),
	}
	if err := s.repo.ReplaceParsedOutput(ctx, markdown, blocks); err != nil {
		if deleteErr := s.storage.Delete(context.WithoutCancel(ctx), storedMarkdown.Key); deleteErr != nil {
			logger.Warn(ctx, "remove uncommitted Markdown failed", "document_id", document.ID, "storage_key", storedMarkdown.Key, "error", deleteErr)
		}
		cleanupImages()
		if errors.Is(err, types.ErrDocumentNotProcessing) {
			logger.Info(ctx, "discard recognized output after status change", "document_id", document.ID)
			return nil
		}
		return fmt.Errorf("persist recognized output: %w", err)
	}
	if previous.StorageKey != "" && previous.StorageKey != storedMarkdown.Key {
		if err := s.storage.Delete(context.WithoutCancel(ctx), previous.StorageKey); err != nil {
			logger.Warn(ctx, "remove replaced Markdown failed", "document_id", document.ID, "storage_key", previous.StorageKey, "error", err)
		}
	}
	for _, block := range previousBlocks {
		if block.AssetKey == "" || block.AssetKey == storedMarkdown.Key {
			continue
		}
		if err := s.storage.Delete(context.WithoutCancel(ctx), block.AssetKey); err != nil {
			logger.Warn(ctx, "remove replaced block image failed", "document_id", document.ID, "block_id", block.ID, "storage_key", block.AssetKey, "error", err)
			continue
		}
		logger.Info(ctx, "replaced document block image deleted", "document_id", document.ID, "block_id", block.ID, "storage_key", block.AssetKey)
	}
	logger.Info(ctx, "document recognition completed", "document_id", document.ID, "block_count", len(blocks), "markdown_storage_key", storedMarkdown.Key)
	if err := s.enqueuePaperInfoAfterRecognition(ctx, document, metadata.SHA256); err != nil {
		return err
	}
	return nil
}

func (s *documentService) enqueuePaperInfoAfterRecognition(
	ctx context.Context,
	document types.Document,
	markdownSHA256 string,
) error {
	if s.taskEnqueuer == nil {
		return nil
	}
	extractionPayload := types.PaperMetadataExtractPayload{
		DocumentID:     document.ID.String(),
		MarkdownSHA256: markdownSHA256,
	}
	task, options, err := NewPaperMetadataExtractTask(
		extractionPayload,
		s.extractionTaskTimeout,
		s.extractionTaskMaxRetry,
	)
	if err != nil {
		return fmt.Errorf("build paper metadata extraction task: %w", err)
	}
	if err := enqueueManagedTask(
		ctx,
		s.taskEnqueuer,
		task,
		options,
		document.ID.String(),
	); err != nil {
		return fmt.Errorf("enqueue paper metadata extraction: %w", err)
	}
	logger.Info(ctx, "paper info handoff completed",
		"document_id", document.ID,
		"markdown_sha256", markdownSHA256,
		"next_task", types.TypePaperMetadataExtract)
	return nil
}

// readRecognitionMarkdown validates and obtains Markdown from either output
// transport. A directory recognizer is allowed to write to its own package on
// a shared volume, but it never chooses the application's durable storage key:
// after this function returns, the service copies the verified bytes through
// FileStorage.SaveBytes.
func (s *documentService) readRecognitionMarkdown(result *types.RecognitionResult) ([]byte, types.RecognitionMetadata, error) {
	if result == nil {
		return nil, types.RecognitionMetadata{}, errors.New("recognizer returned no result")
	}
	metadata := result.Metadata
	filename, err := utils.SafeFileName(metadata.MarkdownFilename)
	if err != nil {
		return nil, metadata, fmt.Errorf("invalid Markdown filename: %w", err)
	}
	if ext := strings.ToLower(filepath.Ext(filename)); ext != ".md" && ext != ".markdown" {
		return nil, metadata, fmt.Errorf("Markdown filename must use .md or .markdown extension")
	}
	metadata.MarkdownFilename = filename
	if metadata.ContentType == "" {
		metadata.ContentType = "text/markdown"
	}
	if !strings.EqualFold(metadata.ContentType, "text/markdown") {
		return nil, metadata, fmt.Errorf("invalid Markdown content type %q", metadata.ContentType)
	}

	var markdown []byte
	switch result.OutputMode {
	case types.RecognitionOutputMarkdownBytes:
		markdown = result.Markdown
	case types.RecognitionOutputDirectory:
		markdown, err = s.readRecognitionMarkdownPackage(result.PackagePath, filename)
		if err != nil {
			return nil, metadata, err
		}
	default:
		return nil, metadata, fmt.Errorf("unsupported recognition output mode %q", result.OutputMode)
	}
	if len(markdown) == 0 {
		return nil, metadata, errors.New("recognizer returned empty Markdown")
	}
	if limit := s.recognitionOutputLimit(); int64(len(markdown)) > limit {
		return nil, metadata, fmt.Errorf("recognizer Markdown exceeds size limit of %d bytes", limit)
	}
	if metadata.SizeBytes > 0 && metadata.SizeBytes != int64(len(markdown)) {
		return nil, metadata, fmt.Errorf("recognizer Markdown size does not match metadata")
	}
	metadata.SizeBytes = int64(len(markdown))
	sum := sha256.Sum256(markdown)
	actualSHA256 := hex.EncodeToString(sum[:])
	if metadata.SHA256 != "" && !strings.EqualFold(metadata.SHA256, actualSHA256) {
		return nil, metadata, fmt.Errorf("recognizer Markdown checksum does not match metadata")
	}
	metadata.SHA256 = actualSHA256
	return markdown, metadata, nil
}

// readRecognitionMarkdownPackage reads one regular Markdown file from a
// configured shared output root. Both lexical and symlink-resolved checks are
// required: Clean alone does not stop a package directory containing a
// symlink that points outside the trusted root.
func (s *documentService) readRecognitionMarkdownPackage(packagePath, filename string) ([]byte, error) {
	if strings.TrimSpace(s.recognitionOutputDir) == "" {
		return nil, errors.New("directory-mode recognition requires RECOGNITION_OUTPUT_DIR")
	}
	root, err := filepath.Abs(filepath.Clean(s.recognitionOutputDir))
	if err != nil {
		return nil, fmt.Errorf("resolve recognition output root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve recognition output root symlinks: %w", err)
	}
	candidate := strings.TrimSpace(packagePath)
	if candidate == "" {
		return nil, errors.New("directory-mode recognition result has no package path")
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	packageDir, err := utils.SafePathUnderBase(root, candidate)
	if err != nil {
		return nil, fmt.Errorf("validate recognition package path: %w", err)
	}
	packageDir, err = filepath.EvalSymlinks(packageDir)
	if err != nil {
		return nil, fmt.Errorf("resolve recognition package symlinks: %w", err)
	}
	if _, err := utils.SafePathUnderBase(root, packageDir); err != nil {
		return nil, fmt.Errorf("validate resolved recognition package path: %w", err)
	}
	info, err := os.Stat(packageDir)
	if err != nil {
		return nil, fmt.Errorf("stat recognition package: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("recognition package path is not a directory")
	}

	markdownPath, err := utils.SafePathUnderBase(packageDir, filepath.Join(packageDir, filename))
	if err != nil {
		return nil, fmt.Errorf("validate recognition Markdown path: %w", err)
	}
	markdownPath, err = filepath.EvalSymlinks(markdownPath)
	if err != nil {
		return nil, fmt.Errorf("resolve recognition Markdown symlinks: %w", err)
	}
	if _, err := utils.SafePathUnderBase(packageDir, markdownPath); err != nil {
		return nil, fmt.Errorf("validate resolved recognition Markdown path: %w", err)
	}
	fileInfo, err := os.Stat(markdownPath)
	if err != nil {
		return nil, fmt.Errorf("stat recognition Markdown: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, errors.New("recognition Markdown is not a regular file")
	}
	limit := s.recognitionOutputLimit()
	if fileInfo.Size() > limit {
		return nil, fmt.Errorf("recognizer Markdown exceeds size limit of %d bytes", limit)
	}
	file, err := os.Open(markdownPath)
	if err != nil {
		return nil, fmt.Errorf("open recognition Markdown: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read recognition Markdown: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("recognizer Markdown exceeds size limit of %d bytes", limit)
	}
	return data, nil
}

func (s *documentService) recognitionOutputLimit() int64 {
	if s.maxRecognitionOutputSize > 0 {
		return s.maxRecognitionOutputSize
	}
	return 100 << 20
}

// failUnsupportedDocumentRecognition handles an immutable configuration or
// capability mismatch. Retrying a PDF through the default text recognizer
// cannot succeed, so persist a user-visible failure and return SkipRetry.
// A database failure remains retryable because recording the status is more
// important than archiving an otherwise invisible failure.
func (s *documentService) failUnsupportedDocumentRecognition(ctx context.Context, documentID uuid.UUID, cause error) error {
	message := "document recognition is not supported for this content type"
	updated, err := s.repo.UpdateStatusIfCurrent(ctx, documentID, types.DocumentStatusFailed, &message,
		[]types.DocumentStatus{types.DocumentStatusProcessing})
	if err != nil {
		return fmt.Errorf("mark unsupported document recognition failed: %w", err)
	}
	if !updated {
		logger.Info(ctx, "unsupported document recognition status already changed", "document_id", documentID)
		return nil
	}
	logger.Warn(ctx, "document recognition rejected as unsupported", "document_id", documentID, "error", cause)
	return fmt.Errorf("unsupported document recognition: %w", asynq.SkipRetry)
}

// failUnavailableDocumentRecognition records a disconnected recognizer as a
// terminal document failure. A connection-refused gRPC call cannot make
// progress within the queued task; automatic retries hide the outage behind a
// processing state. Operators can restore Python and use the normal
// document retry action, which creates a fresh recognition task.
func (s *documentService) failUnavailableDocumentRecognition(ctx context.Context, documentID uuid.UUID, cause error) error {
	message := "document recognition service is unavailable"
	updated, err := s.repo.UpdateStatusIfCurrent(ctx, documentID, types.DocumentStatusFailed, &message,
		[]types.DocumentStatus{types.DocumentStatusProcessing})
	if err != nil {
		return fmt.Errorf("mark unavailable document recognition failed: %w", err)
	}
	if !updated {
		logger.Info(ctx, "unavailable document recognition status already changed", "document_id", documentID)
		return nil
	}
	logger.Error(ctx, "document recognition service is unavailable", "document_id", documentID, "error", cause)
	return fmt.Errorf("document recognition service unavailable: %w", asynq.SkipRetry)
}

// persistBlocks converts parser-owned source-map entries into durable blocks.
// Images are stored before the database transaction because FileStorage may be
// remote; on every later failure ProcessDocument deletes these uncommitted
// objects. The database sees only a protected URL plus a private storage key.
func (s *documentService) persistBlocks(
	ctx context.Context, documentID uuid.UUID, markdownSHA256 string, recognized []types.RecognitionBlock,
) ([]types.Block, []string, error) {
	blocks := make([]types.Block, 0, len(recognized))
	imageKeys := make([]string, 0)
	succeeded := false
	defer func() {
		if !succeeded {
			s.deleteStoredObjects(context.WithoutCancel(ctx), imageKeys, "remove uncommitted block image failed", documentID)
		}
	}()
	for _, block := range recognized {
		if block.Ordinal < 0 || strings.TrimSpace(block.Type) == "" || !isCompletePDFBlockLocation(block.Location) {
			continue
		}
		blockType := strings.ToLower(strings.TrimSpace(block.Type))
		location := block.Location
		persisted := types.Block{
			ID: uuid.New(), DocumentID: documentID, Ordinal: block.Ordinal, Type: blockType,
			Text: block.Text, MarkdownSHA256: markdownSHA256, Location: &location,
		}
		if blockType == "image" || blockType == "table" {
			if !strings.EqualFold(strings.TrimSpace(block.ImageContentType), "image/png") || len(block.ImageData) == 0 {
				return nil, imageKeys, fmt.Errorf("%s block %d has no PNG crop", blockType, block.Ordinal)
			}
			if int64(len(block.ImageData)) > s.recognitionOutputLimit() {
				return nil, imageKeys, fmt.Errorf("%s block %d image exceeds size limit", blockType, block.Ordinal)
			}
			stored, err := s.storage.Save(ctx, interfaces.FileUpload{
				Filename:    fmt.Sprintf("%s-%d.png", documentID, block.Ordinal),
				ContentType: "image/png", Content: bytes.NewReader(block.ImageData),
				Category: interfaces.StorageCategoryImage,
			})
			if err != nil {
				return nil, nil, err
			}
			persisted.AssetKey = stored.Key
			persisted.URL = fmt.Sprintf("/api/document/%s/blocks/%s/image", documentID, persisted.ID)
			imageKeys = append(imageKeys, stored.Key)
			logger.Info(ctx, "document block image stored", "document_id", documentID, "block_id", persisted.ID, "storage_key", stored.Key)
		}
		blocks = append(blocks, persisted)
	}
	succeeded = true
	return blocks, imageKeys, nil
}

func (s *documentService) deleteStoredObjects(ctx context.Context, keys []string, message string, documentID uuid.UUID) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := s.storage.Delete(ctx, key); err != nil {
			logger.Warn(ctx, message, "document_id", documentID, "storage_key", key, "error", err)
		}
	}
}

func isCompletePDFBlockLocation(location types.PDFBlockLocation) bool {
	if location.PageNumber == nil || location.PageWidth == nil || location.PageHeight == nil ||
		location.BBoxX0 == nil || location.BBoxY0 == nil || location.BBoxX1 == nil || location.BBoxY1 == nil ||
		location.MarkdownCodepointStart == nil || location.MarkdownCodepointEnd == nil {
		return false
	}
	return *location.PageNumber > 0 && *location.PageWidth > 0 && *location.PageHeight > 0 &&
		*location.BBoxX0 >= 0 && *location.BBoxY0 >= 0 && *location.BBoxX1 > *location.BBoxX0 &&
		*location.BBoxY1 > *location.BBoxY0 && *location.BBoxX1 <= *location.PageWidth &&
		*location.BBoxY1 <= *location.PageHeight && *location.MarkdownCodepointStart >= 0 &&
		*location.MarkdownCodepointEnd >= *location.MarkdownCodepointStart
}

// CancelDocumentParse changes a cancellable document state to cancelled and
// asks the task backend to discard queued work. Queue cleanup is deliberately
// best-effort: the durable cancelled status remains the source of truth.
func (s *documentService) CancelDocumentParse(ctx context.Context, id string) (*types.Document, error) {
	existing, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		logger.Error(ctx, "load document for cancellation failed", "document_id", id, "error", err)
		return nil, err
	}

	switch existing.Status {
	case types.DocumentStatusCancelled:
		s.dequeueDocumentTasks(ctx, existing.ID.String())
		if s.spans != nil {
			_ = s.spans.CancelDocument(context.WithoutCancel(ctx), existing.ID, "USER_CANCELLED", "document parsing cancelled by user")
		}
		logger.Info(ctx, "document cancellation already applied", "document_id", existing.ID)
		return existing, nil
	case types.DocumentStatusReady, types.DocumentStatusFailed:
		return nil, ErrDocumentParseFinished
	case types.DocumentStatusDeleting:
		return nil, ErrDocumentDeleting
	case types.DocumentStatusUploaded, types.DocumentStatusQueued, types.DocumentStatusProcessing:
		// Cancellable states.
	default:
		logger.Warn(ctx, "cancel document with unexpected status", "document_id", existing.ID, "status", existing.Status)
	}

	message := "document parsing cancelled by user"
	updated, err := s.repo.UpdateStatusIfCurrent(ctx, existing.ID, types.DocumentStatusCancelled, &message,
		[]types.DocumentStatus{types.DocumentStatusUploaded, types.DocumentStatusQueued, types.DocumentStatusProcessing})
	if err != nil {
		logger.Error(ctx, "mark document cancelled failed", "document_id", existing.ID, "error", err)
		return nil, err
	}
	if !updated {
		current, loadErr := s.GetDocumentByID(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		if current.Status == types.DocumentStatusCancelled {
			s.dequeueDocumentTasks(ctx, current.ID.String())
			return current, nil
		}
		if current.Status == types.DocumentStatusReady || current.Status == types.DocumentStatusFailed {
			return nil, ErrDocumentParseFinished
		}
		if current.Status == types.DocumentStatusDeleting {
			return nil, ErrDocumentDeleting
		}
		return current, nil
	}
	existing.Status = types.DocumentStatusCancelled
	existing.ErrorMessage = &message
	logger.Info(ctx, "document marked cancelled", "document_id", existing.ID)
	if s.spans != nil {
		if err := s.spans.CancelDocument(context.WithoutCancel(ctx), existing.ID, "USER_CANCELLED", message); err != nil {
			logger.Warn(ctx, "cancel document processing spans failed", "document_id", existing.ID, "error", err)
		}
	}
	s.dequeueDocumentTasks(ctx, existing.ID.String())
	return existing, nil
}

func (s *documentService) dequeueDocumentTasks(ctx context.Context, documentID string) {
	if s.taskInspector == nil {
		return
	}
	if _, _, err := s.taskInspector.CancelTasksForDocument(ctx, documentID); err != nil {
		logger.Warn(ctx, "dequeue document tasks failed", "document_id", documentID, "error", err)
	}
}

// NewDocumentProcessTask validates and creates a document-recognition task.
// The document service owns its queue, retry, and timeout policy; callers only
// supply the durable payload.
func NewDocumentProcessTask(
	payload types.DocumentProcessPayload,
) (*asynq.Task, []asynq.Option, error) {
	if strings.TrimSpace(payload.DocumentID) == "" {
		return nil, nil, errors.New("document id is required for processing task")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode document processing task: %w", err)
	}
	return asynq.NewTask(types.TypeDocumentProcess, encoded), []asynq.Option{
		asynq.Queue(types.QueueDefault),
		asynq.MaxRetry(defaultDocumentProcessTaskMaxRetry),
		asynq.Timeout(defaultDocumentProcessTaskTimeout),
	}, nil
}

// NewPaperMetadataExtractTask validates and creates an extraction task.
// The paper-information service owns its queue, retry, timeout, and task-ID
// policy; callers only provide the durable payload and optional overrides.
func NewPaperMetadataExtractTask(
	payload types.PaperMetadataExtractPayload,
	timeout time.Duration,
	maxRetry int,
) (*asynq.Task, []asynq.Option, error) {
	if err := payload.Validate(); err != nil {
		return nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultPaperExtractionTaskTimeout
	}
	if maxRetry <= 0 {
		maxRetry = defaultPaperExtractionTaskMaxRetry
	}
	task, err := newPaperInfoJSONTask(types.TypePaperMetadataExtract, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode paper metadata extraction task: %w", err)
	}
	return task, []asynq.Option{
		asynq.Queue(types.QueueDefault),
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(timeout),
		asynq.TaskID(paperInfoTaskID(
			types.TypePaperMetadataExtract,
			payload.DocumentID,
			payload.MarkdownSHA256,
			payload.Attempt,
		)),
	}, nil
}

// NewPaperReferenceDispatchTask validates and creates the lightweight
// bibliography dispatcher. The dispatcher is low priority: it only reads
// durable citation rows and emits provider batches, while source catalog work
// remains on the default queue.
func NewPaperReferenceDispatchTask(
	payload types.PaperReferenceDispatchPayload,
	timeout time.Duration,
	maxRetry int,
) (*asynq.Task, []asynq.Option, error) {
	if strings.TrimSpace(payload.ReferenceRunID) == "" {
		documentID, err := uuid.Parse(strings.TrimSpace(payload.DocumentID))
		if err != nil {
			return nil, nil, fmt.Errorf("parse reference dispatcher document id: %w", err)
		}
		if documentID == uuid.Nil {
			return nil, nil, errors.New("reference dispatcher document id must be non-zero")
		}
		payload.ReferenceRunID = paperReferenceRunID(
			documentID,
			payload.MarkdownSHA256,
			payload.Attempt,
		).String()
	}
	if err := payload.Validate(); err != nil {
		return nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultPaperCatalogTaskTimeout
	}
	if maxRetry <= 0 {
		maxRetry = defaultPaperCatalogTaskMaxRetry
	}
	task, err := newPaperInfoJSONTask(types.TypePaperReferenceDispatch, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode paper reference dispatch task: %w", err)
	}
	return task, []asynq.Option{
		asynq.Queue(types.QueueLow),
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(timeout),
		asynq.TaskID(paperInfoTaskID(
			types.TypePaperReferenceDispatch,
			payload.DocumentID,
			payload.MarkdownSHA256,
			payload.Attempt,
			payload.ReferenceRunID,
		)),
	}, nil
}

// NewPaperCatalogResolveTask creates one single-document catalog task. Source
// work remains on the default queue; scheduler-owned reference work is low
// priority. Provider assignment and fallback state intentionally stay out of
// the payload and are claimed at execution time.
func NewPaperCatalogResolveTask(
	payload types.PaperCatalogResolvePayload,
	timeout time.Duration,
	maxRetry int,
) (*asynq.Task, []asynq.Option, error) {
	if err := payload.Validate(); err != nil {
		return nil, nil, err
	}
	if timeout <= 0 {
		timeout = defaultPaperCatalogTaskTimeout
	}
	if maxRetry <= 0 {
		maxRetry = defaultPaperCatalogTaskMaxRetry
	}
	task, err := newPaperInfoJSONTask(types.TypePaperCatalogResolve, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode paper catalog resolution task: %w", err)
	}
	queue := types.QueueDefault
	if payload.IsReference {
		queue = types.QueueLow
	}
	options := []asynq.Option{
		asynq.Queue(queue),
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(timeout),
	}
	stage := "source"
	if payload.IsReference {
		stage = "reference"
	}
	options = append(options, asynq.TaskID(paperInfoTaskID(
		types.TypePaperCatalogResolve, payload.DocumentID, "", 0, stage,
	)))
	return task, options, nil
}

func newPaperInfoJSONTask(taskType string, payload any) (*asynq.Task, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(taskType, encoded), nil
}

func paperInfoTaskID(taskType, documentID, revision string, attempt int, parts ...string) string {
	stage := strings.NewReplacer(":", "-", "_", "-").Replace(taskType)
	revision = strings.ToLower(strings.TrimSpace(revision))
	base := stage + "-" + strings.TrimSpace(documentID)
	if revision != "" {
		base += "-" + revision
	}
	if attempt > 0 {
		base += "-attempt-" + strconv.Itoa(attempt)
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			base += "-" + strings.NewReplacer(":", "-", "_", "-", "/", "-").Replace(part)
		}
	}
	return base
}

var _ interfaces.DocumentRecognizer
