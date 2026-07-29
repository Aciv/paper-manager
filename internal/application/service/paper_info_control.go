package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// The methods in this file are part of paperInfoService's HTTP-facing control
// plane. They deliberately store no task history: Asynq and dead letters are
// the operational source of truth, while PaperInfoService remains the single
// orchestrator instance for both workers and explicit user actions.
func (s *paperInfoService) GetStatus(
	ctx context.Context,
	documentID string,
) (types.PaperInfoStatus, error) {
	document, metadata, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return types.PaperInfoStatus{}, err
	}
	status := types.PaperInfoStatus{
		DocumentID:     document.ID,
		MetadataSource: metadata.MetadataSource,
		Extraction:     s.extractionState(ctx, document, metadata.MetadataSource),
		Catalog:        s.catalogState(ctx, document, metadata.MetadataSource),
	}
	return status, nil
}

func (s *paperInfoService) RetryExtraction(
	ctx context.Context,
	documentID string,
) (types.PaperInfoStatus, error) {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return types.PaperInfoStatus{}, err
	}
	if document.Status == types.DocumentStatusDeleting {
		return types.PaperInfoStatus{}, ErrPaperInfoDeleting
	}
	if document.Status == types.DocumentStatusUnknown {
		return types.PaperInfoStatus{}, ErrPaperInfoExtractionUnavailable
	}
	markdown, err := s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return types.PaperInfoStatus{}, ErrPaperInfoExtractionUnavailable
	}
	if err != nil {
		return types.PaperInfoStatus{}, err
	}
	task, options, err := NewPaperMetadataExtractTask(
		types.PaperMetadataExtractPayload{
			DocumentID:     document.ID.String(),
			MarkdownSHA256: markdown.SHA256,
			Attempt:        nextPaperInfoManualAttempt(),
		},
		s.cfg.PaperExtractionTaskTimeout,
		s.cfg.PaperExtractionTaskMaxRetry,
	)
	if err != nil {
		return types.PaperInfoStatus{}, fmt.Errorf("build paper metadata extraction task: %w", err)
	}
	if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
		return types.PaperInfoStatus{}, err
	}
	logger.Info(ctx, "paper metadata extraction retry queued", "document_id", document.ID)
	return s.GetStatus(ctx, document.ID.String())
}

func (s *paperInfoService) RetryCatalogResolution(
	ctx context.Context,
	documentID string,
) (types.PaperInfoStatus, error) {
	document, metadata, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return types.PaperInfoStatus{}, err
	}
	if document.Status == types.DocumentStatusDeleting {
		return types.PaperInfoStatus{}, ErrPaperInfoDeleting
	}
	markdownSHA256 := ""
	if document.Status != types.DocumentStatusUnknown {
		if markdown, markdownErr := s.documents.GetMarkdown(ctx, document.ID); markdownErr == nil {
			markdownSHA256 = markdown.SHA256
		} else if !errors.Is(markdownErr, sql.ErrNoRows) {
			return types.PaperInfoStatus{}, markdownErr
		}
	}
	if !hasUsablePaperLookupIdentity(document.DOI, metadata.Title) && !s.hasIncomingReference(ctx, document) {
		return types.PaperInfoStatus{}, ErrPaperInfoCatalogUnavailable
	}
	task, options, err := NewPaperCatalogResolveTask(
		types.PaperCatalogResolvePayload{
			DocumentID:     document.ID.String(),
			MarkdownSHA256: markdownSHA256,
			Attempt:        nextPaperInfoManualAttempt(),
		},
		s.cfg.PaperCatalogTaskTimeout,
		s.cfg.PaperCatalogTaskMaxRetry,
	)
	if err != nil {
		return types.PaperInfoStatus{}, fmt.Errorf("build paper catalog resolution task: %w", err)
	}
	if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
		return types.PaperInfoStatus{}, err
	}
	logger.Info(ctx, "paper catalog resolution retry queued", "document_id", document.ID)
	return s.GetStatus(ctx, document.ID.String())
}

// nextPaperInfoManualAttempt yields a positive, process-monotonic ID for an
// operator retry. Attempt zero remains the deterministic initial task; this
// avoids a collision with a retained dead-letter task for the same Markdown.
func nextPaperInfoManualAttempt() int {
	now := time.Now().UnixNano()
	for {
		previous := paperInfoManualAttempt.Load()
		candidate := now
		if candidate <= previous {
			candidate = previous + 1
		}
		if paperInfoManualAttempt.CompareAndSwap(previous, candidate) {
			return int(candidate)
		}
	}
}

func (s *paperInfoService) loadPaperStatusSource(
	ctx context.Context,
	documentID string,
) (types.Document, *types.PaperMetadata, error) {
	id, err := uuid.Parse(strings.TrimSpace(documentID))
	if err != nil {
		return types.Document{}, nil, fmt.Errorf("invalid document id: %w", err)
	}
	if id == uuid.Nil {
		return types.Document{}, nil, errors.New("invalid document id: UUID must be non-zero")
	}
	document, err := s.documents.Get(ctx, id)
	if err != nil {
		return types.Document{}, nil, err
	}
	metadata, err := s.papers.GetMetadata(ctx, document.ID.String())
	if err != nil {
		return types.Document{}, nil, err
	}
	return document, metadata, nil
}

func (s *paperInfoService) extractionState(
	ctx context.Context,
	document types.Document,
	source types.PaperMetadataSource,
) types.PaperInfoStageStatus {
	if queued, known := s.isQueued(ctx, document.ID.String(), types.TypePaperMetadataExtract); known {
		if queued {
			return types.PaperInfoStageStatus{State: types.PaperInfoStageQueued}
		}
	} else {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageUnknown}
	}

	if document.Status == types.DocumentStatusDeleting || document.Status == types.DocumentStatusUnknown {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageUnavailable}
	}
	if _, err := s.documents.GetMarkdown(ctx, document.ID); errors.Is(err, sql.ErrNoRows) {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageUnavailable}
	} else if err != nil {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageUnknown}
	}
	if source == types.PaperMetadataSourceExtraction || source == types.PaperMetadataSourceCatalog {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageCompleted}
	}
	return types.PaperInfoStageStatus{State: types.PaperInfoStageIdle}
}

func (s *paperInfoService) catalogState(
	ctx context.Context,
	document types.Document,
	source types.PaperMetadataSource,
) types.PaperInfoStageStatus {
	if queued, known := s.isQueued(ctx, document.ID.String(), types.TypePaperCatalogResolve); known {
		if queued {
			return types.PaperInfoStageStatus{State: types.PaperInfoStageQueued}
		}
	} else {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageUnknown}
	}
	if document.Status == types.DocumentStatusDeleting {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageUnavailable}
	}
	if source == types.PaperMetadataSourceCatalog {
		return types.PaperInfoStageStatus{State: types.PaperInfoStageCompleted}
	}
	return types.PaperInfoStageStatus{State: types.PaperInfoStageIdle}
}

// isQueued returns known=false only when queue inspection fails. A nil/no-op
// inspector is a valid Lite-mode observation: no task is queued there.
func (s *paperInfoService) isQueued(
	ctx context.Context,
	documentID, taskType string,
) (queued bool, known bool) {
	if s.inspector == nil {
		return false, true
	}
	queued, err := s.inspector.HasQueuedTaskTypeForDocument(ctx, documentID, taskType)
	if err != nil {
		logger.Warn(ctx, "inspect paper info task failed", "document_id", documentID, "task_type", taskType, "error", err)
		return false, false
	}
	return queued, true
}

func (s *paperInfoService) hasIncomingReference(ctx context.Context, document types.Document) bool {
	if document.Status != types.DocumentStatusUnknown {
		return false
	}
	citations, err := s.papers.ListCitedBy(ctx, document.ID.String())
	if err != nil {
		logger.Warn(ctx, "list incoming references for paper retry failed", "document_id", document.ID, "error", err)
		return false
	}
	return len(citations) > 0
}

var _ interfaces.PaperInfoService = (*paperInfoService)(nil)
