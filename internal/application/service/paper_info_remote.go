package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// DispatchReferenceDownloads performs only bibliography selection and queue
// fan-out. Every target download remains an isolated, independently retryable
// DocumentService task.
func (s *paperInfoService) DispatchReferenceDownloads(
	ctx context.Context, payload types.PaperReferenceDownloadDispatchPayload,
) (outputErr error) {
	if err := payload.Validate(); err != nil {
		return err
	}
	sourceID := uuid.MustParse(payload.SourceDocumentID)
	runID := uuid.MustParse(payload.ReferenceRunID)
	document, err := s.documents.Get(ctx, sourceID)

	if err != nil {
		return err
	}
	if document.Status == types.DocumentStatusUnknown || document.Status == types.DocumentStatusDeleting {
		return ErrPaperInfoCatalogUnavailable
	}
	ctx = logger.WithAttrs(ctx, "document_id", sourceID, "reference_download_run_id", runID)
	attempt := remoteDownloadAttempt(runID)
	var rootSpan uuid.UUID

	enqueued := 0
	if s.spans != nil {
		rootSpan, err = s.spans.Begin(ctx, sourceID, attempt,
			types.ProcessingSpanReferenceDownload, types.ProcessingSpanScopeReferenceDownloadRun,
			types.ComposeProcessingSpanScopeID(types.ProcessingSpanReferenceDownload, runID.String()), retryCount(ctx))
		if err != nil {
			return err
		}
		defer func() {
			if outputErr != nil {
				code, reason := spanFailure(outputErr)
				_ = s.spans.Fail(context.WithoutCancel(ctx), rootSpan, code, reason)
			} else if enqueued == 0 {
				_ = s.spans.Succeed(context.WithoutCancel(ctx), rootSpan)
			}
		}()
	}
	limit := s.cfg.RemotePDFDispatchLimit
	if limit <= 0 {
		limit = 100
	}
	seen := make(map[uuid.UUID]struct{}, limit)
	offset, scanned := 0, 0

	for enqueued < limit {
		page, pageErr := s.papers.ListCitationsPage(ctx, sourceID.String(), offset, 50)
		if pageErr != nil {
			return pageErr
		}
		for _, citation := range page.Items {
			scanned++
			if enqueued >= limit {
				break
			}
			targetID := citation.CitedDocumentID
			if _, duplicate := seen[targetID]; duplicate {
				continue
			}
			seen[targetID] = struct{}{}

			target, getErr := s.documents.Get(ctx, targetID)
			if getErr != nil {
				return getErr
			}
			if target.Status != types.DocumentStatusUnknown {
				continue
			}

			if s.spans != nil {
				active, activeErr := s.spans.HasActive(ctx, targetID, types.ProcessingSpanReferenceDownload)
				if activeErr != nil {
					return activeErr
				}
				if active {
					continue
				}
			}

			metadata, metadataErr := s.papers.GetMetadata(ctx, targetID.String())
			if metadataErr != nil {
				return metadataErr
			}
			if strings.TrimSpace(target.DOI) == "" && strings.TrimSpace(metadata.SourceURL) == "" {
				continue
			}

			taskPayload := types.DocumentRemoteImportPayload{
				RequestID: payload.RequestID, DocumentID: targetID.String(), Attempt: attempt,
			}
			task, options, buildErr := NewDocumentRemoteImportTask(
				taskPayload,
				s.cfg.RemotePDFTaskTimeout,
				s.cfg.RemotePDFTaskMaxRetry)

			if buildErr != nil {
				return buildErr
			}
			if s.spans != nil && rootSpan != uuid.Nil {
				_, spanErr := s.spans.BeginChild(ctx, rootSpan, targetID, attempt,
					types.ProcessingSpanReferenceDownload, types.ProcessingSpanScopeRemoteDocument,
					remoteDocumentScopeID(targetID, attempt), 0)
				if spanErr != nil {
					return spanErr
				}
			}
			// enqueue download task
			if err := enqueueManagedTask(ctx, s.tasks, task, options, targetID.String()); err != nil {
				return err
			}
			enqueued++
		}
		if !page.HasMore || page.NextOffset <= offset {
			break
		}
		offset = page.NextOffset
	}
	logger.Info(ctx, "reference PDF downloads dispatched", "citation_count", scanned, "download_count", enqueued)
	return nil
}

// CancelReferenceDownloads cancels only bibliography acquisition roots and
// their child target spans. A queued worker observes the cancelled marker and
// revokes itself before resolving or downloading a URL.
func (s *paperInfoService) CancelReferenceDownloads(ctx context.Context, documentID string) error {
	id, err := uuid.Parse(strings.TrimSpace(documentID))
	if err != nil || id == uuid.Nil {
		return errors.New("invalid document id")
	}
	if s.spans == nil {
		return ErrPaperInfoCatalogUnavailable
	}
	runs, err := s.spans.CancelReferenceDownloadRuns(context.WithoutCancel(ctx), id,
		"USER_CANCELLED", "reference PDF acquisition cancelled by user")
	if err != nil {
		return err
	}
	logger.Info(ctx, "reference PDF acquisition runs cancelled", "document_id", id, "run_count", len(runs))
	return nil
}

func remoteDownloadAttempt(runID uuid.UUID) int {
	// Span attempts are positive integers. Folding the first 31 UUID bits keeps
	// independently cancellable runs distinct without putting run_id into each
	// single-document download payload.
	value := int(runID[0])<<24 | int(runID[1])<<16 | int(runID[2])<<8 | int(runID[3])
	value &= 0x7fffffff
	if value < 1 {
		return 1
	}
	return value
}

func remoteDocumentScopeID(documentID uuid.UUID, attempt int) types.ProcessingSpanScopeID {
	return types.ComposeProcessingSpanScopeID(types.ProcessingSpanReferenceDownload,
		documentID.String(), strconv.Itoa(positiveAttempt(attempt)))
}
