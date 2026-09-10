package service

import (
	"bytes"
	"context"
	"database/sql"
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
	defaultDocumentRecognitionTimeout  = 15 * time.Minute
	defaultDocumentRecognitionMaxRetry = 3

	defaultPaperExtractionTaskTimeout  = 2 * time.Minute
	defaultPaperExtractionTaskMaxRetry = 3
	defaultPaperCatalogTaskTimeout     = 10 * time.Minute
	defaultPaperCatalogTaskMaxRetry    = 5
	defaultRemotePDFTaskTimeout        = 5 * time.Minute
	defaultRemotePDFTaskMaxRetry       = 4
)

// RecognizeDocument is the dedicated recognition worker. The parser's
// Markdown and source-map blocks are committed together, so readers never see
// a ready Markdown revision paired with stale PDF coordinates.
func (s *documentService) RecognizeDocument(ctx context.Context, task *asynq.Task) (processErr error) {
	if task == nil {
		return errors.New("document processing task is required")
	}
	var payload types.DocumentProcessPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode document processing payload: %w", err)
	}
	return s.recognizeDocument(ctx, payload)
}

// recognizeDocument owns only file recognition and parsed-output persistence.
func (s *documentService) recognizeDocument(ctx context.Context, payload types.DocumentProcessPayload) (processErr error) {
	documentID, err := uuid.Parse(strings.TrimSpace(payload.DocumentID))
	if err != nil {
		return fmt.Errorf("invalid document id in processing payload: %w", err)
	}
	if err := types.ValidateDocumentStageEnvelope(payload.Stage, payload.Generation, types.DocumentStageMarkdown); err != nil {
		return err
	}
	_, err = startDocumentStageTask(ctx, s.repo, documentID, payload.Stage, payload.Generation)
	if err != nil {
		return err
	}
	defer func() {
		finishDocumentStageTask(ctx, s.repo, documentID, payload.Stage, payload.Generation, processErr)
	}()
	document, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return fmt.Errorf("%w: document source is %s", asynq.RevokeTask, document.SourceState)
	}
	attempt := stageAttempt(payload.Generation, 0)
	retryCount, _ := asynq.GetRetryCount(ctx)
	var spanID uuid.UUID
	if s.spans != nil {
		var spanErr error
		spanID, spanErr = s.spans.BeginSpan(ctx, document.ID, attempt, types.ProcessingSpanRecognition, types.ProcessingSpanScopeDocument, types.ComposeProcessingSpanScopeID(types.ProcessingSpanRecognition, document.ID.String()), retryCount)
		if spanErr != nil {
			logger.Warn(ctx, "start recognition processing span failed", "document_id", document.ID, "error", spanErr)
		}
	}
	stopHeartbeat := keepSpanAlive(ctx, s.spans, spanID)
	defer stopHeartbeat()
	defer func() {
		if s.spans == nil {
			return
		}
		if processErr != nil {
			code, reason := spanFailure(processErr)
			if err := s.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanFailed, code, reason); err != nil {
				logger.Warn(ctx, "mark recognition processing span failed", "document_id", document.ID, "error", err)
			}
			return
		}
		if err := s.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanSucceeded, "", ""); err != nil {
			logger.Warn(ctx, "finish recognition processing span failed", "document_id", document.ID, "error", err)
		}
	}()
	if s.storage == nil || s.recognizer == nil {
		return errors.New("document recognition dependencies are not configured")
	}
	if err := ensureDocumentStageWork(ctx, s.repo, documentID, payload.Stage, payload.Generation); err != nil {
		return err
	}

	logger.Info(ctx, "start document recognition", "document_id", document.ID, "content_type", document.ContentType)
	input, err := s.storage.Open(ctx, document.StorageKey)
	if err != nil {
		return fmt.Errorf("open document source: %w", err)
	}
	if err := ensureDocumentStageWork(ctx, s.repo, documentID, payload.Stage, payload.Generation); err != nil {
		return err
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
	if err := ensureDocumentStageWork(ctx, s.repo, documentID, payload.Stage, payload.Generation); err != nil {
		return err
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
			return fmt.Errorf("%w: document was deleted during recognition", asynq.RevokeTask)
		}
		return fmt.Errorf("reload document after recognition: %w", err)
	}
	if current.SourceState != types.DocumentSourceAvailable {
		return fmt.Errorf("%w: document source changed to %s", asynq.RevokeTask, current.SourceState)
	}
	if err := ensureDocumentStageWork(ctx, s.repo, document.ID, payload.Stage, payload.Generation); err != nil {
		return err
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
	blocks, imageKeys, err := s.persistBlocks(ctx, document.ID, result.Blocks)
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
		ContentType: metadata.ContentType, SizeBytes: storedMarkdown.Size,
		ParserName: metadata.ParserName, ParserVersion: metadata.ParserVersion,
		InputMode: string(types.RecognitionInputBytes), Language: metadata.Language,
		PageCount: metadata.PageCount,
		// metadata is a non-null JSONB column. An empty named RawMessage can be
		// serialized by GORM as an invalid database JSON value, whereas an empty
		// object faithfully represents that this parser result has no extra
		// metadata yet.
		Metadata: types.JSON(`{}`),
	}

	if err := s.repo.ReplaceParsedOutput(ctx, markdown, blocks, payload.Generation); err != nil {
		if deleteErr := s.storage.Delete(context.WithoutCancel(ctx), storedMarkdown.Key); deleteErr != nil {
			logger.Warn(ctx, "remove uncommitted Markdown failed", "document_id", document.ID, "storage_key", storedMarkdown.Key, "error", deleteErr)
		}
		cleanupImages()
		if errors.Is(err, types.ErrDocumentNotProcessing) {
			logger.Info(ctx, "discard recognized output after status change", "document_id", document.ID)
			return fmt.Errorf("%w: Markdown stage no longer permits output commit", asynq.RevokeTask)
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
	if err := s.enqueueMetadataExtractionIfNeeded(ctx, document.ID); err != nil {
		logger.Warn(ctx, "queue metadata extraction after recognition failed", "document_id", document.ID, "error", err)
	}
	return nil
}

func (s *documentService) enqueueMetadataExtractionIfNeeded(ctx context.Context, documentID uuid.UUID) error {
	state, err := s.repo.GetStage(ctx, documentID, types.DocumentStageMetadataExtraction)
	if err != nil {
		return err
	}
	if state.Status == types.DocumentStageNotStarted {
		state, _, err = s.repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMetadataExtraction)
		if err != nil {
			return err
		}
	}
	if state.Status != types.DocumentStageQueued {
		return nil
	}
	payload := types.PaperMetadataExtractPayload{
		DocumentID: documentID.String(),
		Stage:      types.DocumentStageMetadataExtraction, Generation: state.Generation,
	}
	task, options, err := NewPaperMetadataExtractTask(payload, 0, 0)
	if err == nil {
		err = enqueueDocumentStageTask(ctx, s.taskEnqueuer, task, options, documentID.String())
	}
	if err != nil {
		_, _, _ = s.repo.FinishStage(context.WithoutCancel(ctx), documentID,
			types.DocumentStageMetadataExtraction, state.Generation, types.DocumentStageFailed, types.ProcessingErrorTaskFailed, "processing task could not be queued")
	}
	return err
}

func (s *documentService) finishDocumentProcess(ctx context.Context, processID uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error {
	if s.spans == nil || processID == uuid.Nil {
		return nil
	}
	var err error
	switch status {
	case types.ProcessingSpanSucceeded:
		err = s.spans.FinishSpan(context.WithoutCancel(ctx), processID, types.ProcessingSpanSucceeded, "", "")
	case types.ProcessingSpanCancelled:
		err = s.spans.FinishSpan(context.WithoutCancel(ctx), processID, types.ProcessingSpanCancelled, code, reason)
	default:
		err = s.spans.FinishSpan(context.WithoutCancel(ctx), processID, types.ProcessingSpanFailed, code, reason)
	}
	if err != nil {
		return fmt.Errorf("finish document process: %w", err)
	}
	logger.Info(ctx, "document process completed", "process_id", processID, "status", status)
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
	logger.Warn(ctx, "document recognition rejected as unsupported", "document_id", documentID, "error", cause)
	return fmt.Errorf("%w: %w", asynq.SkipRetry, cause)
}

// failUnavailableDocumentRecognition records a disconnected recognizer as a
// terminal document failure. A connection-refused gRPC call cannot make
// progress within the queued task; automatic retries hide the outage behind a
// processing state. Operators can restore Python and use the normal
// document retry action, which creates a fresh recognition task.
func (s *documentService) failUnavailableDocumentRecognition(ctx context.Context, documentID uuid.UUID, cause error) error {
	logger.Error(ctx, "document recognition service is unavailable", "document_id", documentID, "error", cause)
	return fmt.Errorf("%w: %w", asynq.SkipRetry, cause)
}

// persistBlocks converts parser-owned source-map entries into durable blocks.
// Images are stored before the database transaction because FileStorage may be
// remote; on every later failure ProcessDocument deletes these uncommitted
// objects. The database sees only a protected URL plus a private storage key.
func (s *documentService) persistBlocks(
	ctx context.Context, documentID uuid.UUID, recognized []types.RecognitionBlock,
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
			Text: block.Text, Location: &location,
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

// CancelDocumentParse fences only the Markdown stage. The fence is committed
// before queue cleanup, so a Redis outage cannot permit a late worker write.
func (s *documentService) CancelDocumentParse(ctx context.Context, id string) (*types.Document, error) {
	existing, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		logger.Error(ctx, "load document for cancellation failed", "document_id", id, "error", err)
		return nil, err
	}
	if existing.SourceState == types.DocumentSourceDeleting || existing.SourceState == types.DocumentSourceMerged {
		return nil, ErrDocumentDeleting
	}
	stage, err := s.repo.GetStage(ctx, existing.ID, types.DocumentStageMarkdown)
	if err != nil {
		return nil, err
	}
	if stage.Status.Active() {
		cancelledStage, applied, err := s.repo.CancelStage(ctx, existing.ID, stage.Stage, stage.Generation)
		if err != nil {
			return nil, err
		}
		if applied {
			stage = cancelledStage
		} else {
			stage, err = s.repo.GetStage(ctx, existing.ID, stage.Stage)
			if err != nil {
				return nil, err
			}
		}
	}
	if stage.Status != types.DocumentStageCancelled {
		return nil, ErrDocumentParseFinished
	}
	s.cancelStageProjectionAndTasks(ctx, existing.ID, stage.Stage, stage.Generation)
	updated, err := s.GetDocumentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "document markdown cancellation completed", "document_id", existing.ID, "generation", stage.Generation)
	return updated, nil
}

func (s *documentService) cancelStageProjectionAndTasks(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64) {
	cancelStageProjectionAndTasks(ctx, s.taskInspector, s.spans, documentID, stage, generation)
}

func cancelStageProjectionAndTasks(ctx context.Context, inspector interfaces.TaskInspector, spans interfaces.SpanRecorder, documentID uuid.UUID, stage types.DocumentStage, generation int64) {
	if inspector != nil {
		if _, _, err := inspector.CancelTasksForStage(context.WithoutCancel(ctx), documentID.String(), stage, generation); err != nil {
			logger.Warn(ctx, "cancel document stage tasks failed", "document_id", documentID, "stage", stage, "generation", generation, "error", err)
		}
	}
	if spans != nil {
		if err := spans.CancelStageProjection(context.WithoutCancel(ctx), documentID, stage, generation, types.ProcessingErrorUserCancelled, "processing cancelled by user"); err != nil {
			logger.Warn(ctx, "cancel stage span projection failed", "document_id", documentID, "stage", stage, "generation", generation, "error", err)
		}
	}
}

// NewDocumentRecognitionTask creates the dedicated recognition worker task.
func NewDocumentRecognitionTask(payload types.DocumentProcessPayload) (*asynq.Task, []asynq.Option, error) {
	if strings.TrimSpace(payload.DocumentID) == "" {
		return nil, nil, errors.New("document id is required for recognition task")
	}
	if err := types.ValidateDocumentStageEnvelope(payload.Stage, payload.Generation, types.DocumentStageMarkdown); err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode document recognition task: %w", err)
	}
	options := []asynq.Option{
		asynq.Queue(types.QueueDefault),
		asynq.MaxRetry(defaultDocumentRecognitionMaxRetry),
		asynq.Timeout(defaultDocumentRecognitionTimeout),
	}
	options = append(options, asynq.TaskID(paperInfoTaskID(
		types.TypeDocumentRecognize, payload.DocumentID, 0, string(payload.Stage), strconv.FormatInt(payload.Generation, 10),
	)))
	return asynq.NewTask(types.TypeDocumentRecognize, encoded), options, nil
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
	parts := stageTaskIDParts(payload.Stage, payload.Generation)
	return task, []asynq.Option{
		asynq.Queue(types.QueueDefault),
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(timeout),
		asynq.TaskID(paperInfoTaskID(
			types.TypePaperMetadataExtract,
			payload.DocumentID,
			0,
			parts...,
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
	parts := stageTaskIDParts(payload.Stage, payload.Generation)
	return task, []asynq.Option{
		asynq.Queue(types.QueueLow),
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(timeout),
		asynq.TaskID(paperInfoTaskID(
			types.TypePaperReferenceDispatch,
			payload.DocumentID,
			0,
			parts...,
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
	options := []asynq.Option{
		asynq.Queue(types.QueueDefault),
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(timeout),
	}
	parts := stageTaskIDParts(payload.Stage, payload.Generation)
	options = append(options, asynq.TaskID(paperInfoTaskID(
		types.TypePaperCatalogResolve, payload.DocumentID, 0, parts...,
	)))
	return task, options, nil
}

// NewDocumentRemoteImportTask is intentionally used only by the explicit
// reference-download action, never by reference parsing or catalog lookup.
func NewDocumentRemoteImportTask(payload types.DocumentRemoteImportPayload) (*asynq.Task, []asynq.Option, error) {
	if err := payload.Validate(); err != nil {
		return nil, nil, err
	}
	task, err := newPaperInfoJSONTask(types.TypeDocumentRemoteImport, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("encode remote document import task: %w", err)
	}
	return task, []asynq.Option{
		asynq.Queue(types.QueueDownload), asynq.Timeout(defaultRemotePDFTaskTimeout), asynq.MaxRetry(defaultRemotePDFTaskMaxRetry),
		asynq.TaskID(paperInfoTaskID(types.TypeDocumentRemoteImport, payload.DocumentID, 0, strconv.FormatInt(payload.Generation, 10))),
	}, nil
}

func newPaperInfoJSONTask(taskType string, payload any) (*asynq.Task, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(taskType, encoded), nil
}

func paperInfoTaskID(taskType, documentID string, attempt int, parts ...string) string {
	stage := strings.NewReplacer(":", "-", "_", "-").Replace(taskType)
	base := stage + "-" + strings.TrimSpace(documentID)
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

func stageTaskIDParts(stage types.DocumentStage, generation int64) []string {
	if !types.HasDocumentStageEnvelope(stage, generation) {
		return nil
	}
	return []string{string(stage), strconv.FormatInt(generation, 10)}
}

var _ interfaces.DocumentRecognizer
