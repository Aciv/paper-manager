package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

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
		return fmt.Errorf("recognize document: %w", err)
	}
	if result == nil || result.OutputMode != types.RecognitionOutputMarkdownBytes || len(result.Markdown) == 0 {
		return errors.New("recognizer returned no Markdown bytes")
	}

	previous, err := s.repo.GetMarkdown(ctx, document.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load previous Markdown: %w", err)
	}
	storedMarkdown, err := s.storage.SaveBytes(ctx, result.Metadata.MarkdownFilename, result.Markdown)
	if err != nil {
		return fmt.Errorf("store recognized Markdown: %w", err)
	}
	markdown := types.DocumentMarkdown{
		DocumentID: document.ID, StorageKey: storedMarkdown.Key, Filename: storedMarkdown.Filename,
		ContentType: "text/markdown", SizeBytes: storedMarkdown.Size, SHA256: result.Metadata.SHA256,
		ParserName: result.Metadata.ParserName, ParserVersion: result.Metadata.ParserVersion,
		InputMode: string(types.RecognitionInputBytes), Language: result.Metadata.Language,
		PageCount: result.Metadata.PageCount, Metadata: types.JSON(`{}`),
	}
	blocks := persistedBlocks(document.ID, result.Metadata.SHA256, result.Blocks)
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
