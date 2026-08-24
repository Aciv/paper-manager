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
// plane. Processing state is deliberately read from the persistent span
// timeline, rather than inferred from an Asynq queue snapshot.

func (s *paperInfoService) RetryExtraction(
	ctx context.Context,
	documentID string,
) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusDeleting {
		return ErrPaperInfoDeleting
	}
	if document.Status == types.DocumentStatusUnknown {
		return ErrPaperInfoExtractionUnavailable
	}
	if active, err := s.paperStageActive(ctx, document.ID, types.ProcessingSpanMetadataExtract, types.TypePaperMetadataExtract); err != nil {
		return err
	} else if active {
		return ErrPaperInfoAttemptActive
	}
	_, err = s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperInfoExtractionUnavailable
	}
	if err != nil {
		return err
	}
	task, options, err := NewPaperMetadataExtractTask(
		types.PaperMetadataExtractPayload{
			DocumentID: document.ID.String(),
			Attempt:    nextPaperInfoManualAttempt(),
		},
		s.cfg.PaperExtractionTaskTimeout,
		s.cfg.PaperExtractionTaskMaxRetry,
	)
	if err != nil {
		return fmt.Errorf("build paper metadata extraction task: %w", err)
	}
	if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
		return err
	}
	logger.Info(ctx, "paper metadata extraction retry queued", "document_id", document.ID)
	return nil
}

func (s *paperInfoService) RetryCatalogResolution(
	ctx context.Context,
	documentID string,
) error {
	document, metadata, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusDeleting {
		return ErrPaperInfoDeleting
	}
	// Unknown placeholders do not own source-paper metadata. Their bibliography
	// work is retried through RetryReferenceResolution on the citing document.
	if document.Status == types.DocumentStatusUnknown {
		return ErrPaperInfoCatalogUnavailable
	}
	if active, err := s.paperStageActive(ctx, document.ID, types.ProcessingSpanCatalogSource, types.TypePaperCatalogResolve); err != nil {
		return err
	} else if active {
		return ErrPaperInfoAttemptActive
	}
	if !hasUsablePaperLookupIdentity(document.DOI, metadata.Title) {
		return ErrPaperInfoCatalogUnavailable
	}
	task, options, err := NewPaperCatalogResolveTask(
		types.PaperCatalogResolvePayload{
			DocumentID: document.ID.String(),
		},
		s.cfg.PaperCatalogTaskTimeout,
		s.cfg.PaperCatalogTaskMaxRetry,
	)
	if err != nil {
		return fmt.Errorf("build paper catalog resolution task: %w", err)
	}
	if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
		return err
	}
	logger.Info(ctx, "paper catalog resolution retry queued", "document_id", document.ID)
	return nil
}

// RetryReferenceResolution retries only the bibliography dispatcher for a
// source document. The dispatcher reloads citations, registers scheduler jobs,
// and queues provider-owned catalog tasks; it does not repeat extraction or
// source-paper lookup.
func (s *paperInfoService) RetryReferenceResolution(ctx context.Context, documentID string) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusDeleting {
		return ErrPaperInfoDeleting
	}
	if document.Status == types.DocumentStatusUnknown {
		return ErrPaperInfoCatalogUnavailable
	}
	if active, err := s.paperStageActive(ctx, document.ID, types.ProcessingSpanCatalogReference, types.TypePaperReferenceDispatch); err != nil {
		return err
	} else if active {
		return ErrPaperInfoAttemptActive
	}
	_, err = s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperInfoCatalogUnavailable
	}
	if err != nil {
		return err
	}
	payload := types.PaperReferenceDispatchPayload{
		DocumentID: document.ID.String(),
		Attempt:    nextPaperInfoManualAttempt(),
	}
	task, options, err := NewPaperReferenceDispatchTask(payload, s.cfg.PaperCatalogTaskTimeout, s.cfg.PaperCatalogTaskMaxRetry)
	if err != nil {
		return fmt.Errorf("build paper reference dispatch task: %w", err)
	}
	if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
		return err
	}
	logger.Info(ctx, "paper reference resolution retry queued", "document_id", document.ID)
	return nil
}

// Cancel stops only active reference-catalog runs. Metadata extraction and
// source-paper lookup deliberately run to their terminal state, and the ready
// document remains ready throughout.
func (s *paperInfoService) Cancel(ctx context.Context, documentID string) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusDeleting {
		return ErrPaperInfoDeleting
	}
	if s.spans == nil {
		return ErrPaperInfoCatalogUnavailable
	}
	runs, err := s.spans.CancelReferenceRuns(context.WithoutCancel(ctx), document.ID, "USER_CANCELLED", "reference catalog run cancelled by user")
	if err != nil {
		return err
	}
	for _, runID := range runs {
		if s.referenceScheduler != nil {
			targets, cancelErr := s.referenceScheduler.CancelRun(context.WithoutCancel(ctx), document.ID, runID)
			if cancelErr != nil {
				return cancelErr
			}
			if s.inspector != nil && len(targets) > 0 {
				ids := make([]string, 0, len(targets))
				for _, target := range targets {
					ids = append(ids, target.String())
				}
				if _, _, cancelErr := s.inspector.CancelReferenceTasksForDocuments(ctx, ids); cancelErr != nil {
					logger.Warn(ctx, "cancel scheduler-owned reference tasks failed", "document_id", document.ID, "reference_run_id", runID, "error", cancelErr)
				}
			}
		}
		if s.inspector == nil {
			continue
		}
		if _, _, err := s.inspector.CancelReferenceTasksForRun(ctx, document.ID.String(), runID.String()); err != nil {
			logger.Warn(ctx, "cancel reference catalog run tasks failed", "document_id", document.ID, "reference_run_id", runID, "error", err)
		}
	}
	logger.Info(ctx, "reference catalog runs cancelled", "document_id", document.ID, "run_count", len(runs))
	return nil
}

func (s *paperInfoService) paperStageActive(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, taskType string) (bool, error) {
	if s.spans != nil {
		active, err := s.spans.HasActive(ctx, documentID, kind)
		if err != nil {
			return false, err
		}
		if active {
			return true, nil
		}
	}
	if s.inspector == nil {
		return false, nil
	}
	return s.inspector.HasQueuedTaskTypeForDocument(ctx, documentID.String(), taskType)
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

var _ interfaces.PaperInfoService = (*paperInfoService)(nil)
