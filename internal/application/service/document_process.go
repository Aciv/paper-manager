package service

import (
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
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// ProcessDocument is the Asynq entry point for recognition. The parser's
// Markdown and source-map blocks are committed together, so readers never see
// a ready Markdown revision paired with stale PDF coordinates.
func (s *documentService) ProcessDocument(ctx context.Context, task *asynq.Task) error {
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
		logger.Info(ctx, "skip duplicate document recognition task", "document_id", document.ID)
		return nil
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
	storedMarkdown, err := s.storage.SaveBytes(ctx, metadata.MarkdownFilename, markdownBytes)
	if err != nil {
		return fmt.Errorf("store recognized Markdown: %w", err)
	}
	markdown := types.DocumentMarkdown{
		DocumentID: document.ID, StorageKey: storedMarkdown.Key, Filename: storedMarkdown.Filename,
		ContentType: metadata.ContentType, SizeBytes: storedMarkdown.Size, SHA256: metadata.SHA256,
		ParserName: metadata.ParserName, ParserVersion: metadata.ParserVersion,
		InputMode: string(types.RecognitionInputBytes), Language: metadata.Language,
		PageCount: metadata.PageCount, Metadata: types.JSON(`{}`),
	}
	blocks := persistedBlocks(document.ID, metadata.SHA256, result.Blocks)
	if err := s.repo.ReplaceParsedOutput(ctx, markdown, blocks); err != nil {
		if deleteErr := s.storage.Delete(context.WithoutCancel(ctx), storedMarkdown.Key); deleteErr != nil {
			logger.Warn(ctx, "remove uncommitted Markdown failed", "document_id", document.ID, "storage_key", storedMarkdown.Key, "error", deleteErr)
		}
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
	logger.Info(ctx, "document recognition completed", "document_id", document.ID, "block_count", len(blocks), "markdown_storage_key", storedMarkdown.Key)
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

func persistedBlocks(documentID uuid.UUID, markdownSHA256 string, recognized []types.RecognitionBlock) []types.Block {
	blocks := make([]types.Block, 0, len(recognized))
	for _, block := range recognized {
		if block.Ordinal < 0 || strings.TrimSpace(block.Type) == "" || !isCompletePDFBlockLocation(block.Location) {
			continue
		}
		location := block.Location
		blocks = append(blocks, types.Block{
			ID: uuid.New(), DocumentID: documentID, Ordinal: block.Ordinal, Type: block.Type,
			Text: block.Text, MarkdownSHA256: markdownSHA256, Location: &location,
		})
	}
	return blocks
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

var _ interfaces.DocumentRecognizer
