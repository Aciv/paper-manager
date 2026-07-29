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
	markdown, err := s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperInfoExtractionUnavailable
	}
	if err != nil {
		return err
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
	if active, err := s.paperStageActive(ctx, document.ID, types.ProcessingSpanCatalogSource, types.TypePaperCatalogResolve); err != nil {
		return err
	} else if active {
		return ErrPaperInfoAttemptActive
	}
	if document.Status == types.DocumentStatusUnknown {
		queued, err := s.enqueueUnknownReferenceCatalogRetries(ctx, document)
		if err != nil {
			return err
		}
		if queued == 0 {
			return ErrPaperInfoCatalogUnavailable
		}
		logger.Info(ctx, "unknown paper reference catalog retries queued", "document_id", document.ID, "queued_count", queued)
		return nil
	}
	markdownSHA256 := ""
	if markdown, markdownErr := s.documents.GetMarkdown(ctx, document.ID); markdownErr == nil {
		markdownSHA256 = markdown.SHA256
	} else if !errors.Is(markdownErr, sql.ErrNoRows) {
		return markdownErr
	}
	if !hasUsablePaperLookupIdentity(document.DOI, metadata.Title) {
		return ErrPaperInfoCatalogUnavailable
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
		return fmt.Errorf("build paper catalog resolution task: %w", err)
	}
	if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
		return err
	}
	logger.Info(ctx, "paper catalog resolution retry queued", "document_id", document.ID)
	return nil
}

// Cancel stops independent paper-information work. It intentionally leaves a
// ready document ready: parsed Markdown and blocks remain available.
func (s *paperInfoService) Cancel(ctx context.Context, documentID string) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusDeleting {
		return ErrPaperInfoDeleting
	}
	if s.inspector != nil {
		if _, _, err := s.inspector.CancelTasksForDocument(ctx, document.ID.String()); err != nil {
			logger.Warn(ctx, "cancel paper information tasks failed", "document_id", document.ID, "error", err)
		}
	}
	if s.spans != nil {
		if err := s.spans.CancelDocument(context.WithoutCancel(ctx), document.ID, "USER_CANCELLED", "paper information processing cancelled by user"); err != nil {
			return err
		}
	}
	logger.Info(ctx, "paper information processing cancelled", "document_id", document.ID)
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

// enqueueUnknownReferenceCatalogRetries retries an unknown placeholder through
// each source document that cites it. A placeholder has no Markdown/file of
// its own, so source-scope work would have no durable query context and must
// never be queued for it.
func (s *paperInfoService) enqueueUnknownReferenceCatalogRetries(
	ctx context.Context,
	placeholder types.Document,
) (int, error) {
	if s.tasks == nil {
		return 0, errors.New("task queue is not configured")
	}
	incoming, err := s.papers.ListCitedBy(ctx, placeholder.ID.String())
	if err != nil {
		return 0, fmt.Errorf("list incoming references: %w", err)
	}
	attempt := nextPaperInfoManualAttempt()
	seen := make(map[string]struct{}, len(incoming))
	queued := 0
	for _, citation := range incoming {
		if err := ctx.Err(); err != nil {
			return queued, err
		}
		if citation.CitingDocumentID == uuid.Nil || citation.CitingDocumentID == placeholder.ID {
			continue
		}
		key := citation.CitingDocumentID.String() + "\x00" + citation.RawReference
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}

		source, err := s.documents.Get(ctx, citation.CitingDocumentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return queued, err
		}
		if source.Status == types.DocumentStatusDeleting || source.Status == types.DocumentStatusUnknown {
			continue
		}
		markdownSHA256 := ""
		if markdown, err := s.documents.GetMarkdown(ctx, source.ID); err == nil {
			markdownSHA256 = markdown.SHA256
		} else if !errors.Is(err, sql.ErrNoRows) {
			return queued, err
		}
		task, options, err := NewPaperCatalogResolveTask(
			types.PaperCatalogResolvePayload{
				DocumentID:      source.ID.String(),
				MarkdownSHA256:  markdownSHA256,
				Attempt:         attempt,
				Scope:           types.PaperCatalogScopeReference,
				CitedDocumentID: placeholder.ID.String(),
			},
			s.cfg.PaperCatalogTaskTimeout,
			s.cfg.PaperCatalogTaskMaxRetry,
		)
		if err != nil {
			return queued, fmt.Errorf("build unknown reference catalog task: %w", err)
		}
		if err := enqueueManagedTask(ctx, s.tasks, task, options, source.ID.String()); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
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
