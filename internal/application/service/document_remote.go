package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// CreateDocumentFromURL imports an explicit remote PDF through the same
// durable document lifecycle as multipart upload.
func (s *documentService) CreateDocumentFromURL(
	ctx context.Context, command types.DocumentURLImportCommand,
) (*types.Document, error) {
	if err := command.Validate(); err != nil {
		return nil, err
	}
	if s.urlResolver == nil {
		return nil, errors.New("paper URL resolver is not configured")
	}
	resolved, err := s.urlResolver.Resolve(ctx, types.PaperURLResolveRequest{
		SourceURL: command.URL, ExplicitDOI: command.DOI,
	})
	if err != nil {
		return nil, err
	}
	filename := strings.TrimSpace(command.Filename)
	if filename == "" {
		filename = resolved.Filename
	}
	if safe, safeErr := utils.SafeFileName(filename); safeErr != nil || !strings.EqualFold(filepath.Ext(safe), ".pdf") {
		return nil, ErrInvalidDocumentFilename
	}
	if duplicate, err := s.findDuplicateDocument(ctx, uuid.Nil, resolved.DOI, ""); err != nil {
		return nil, err
	} else if duplicate != uuid.Nil {
		return nil, ErrDuplicateDOI
	}
	stored, err := s.downloadResolvedPDF(ctx, resolved.DownloadURL, filename)
	if err != nil {
		return nil, err
	}
	if duplicate, searchErr := s.findDuplicateDocument(ctx, uuid.Nil, "", stored.File.SHA256); searchErr != nil {
		_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		return nil, searchErr
	} else if duplicate != uuid.Nil {
		_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		return nil, ErrDuplicateFileHash
	}
	document := types.Document{
		ID: uuid.New(), OriginalFilename: stored.File.Filename,
		ContentType: stored.File.ContentType, SizeBytes: stored.File.Size,
		DOI: resolved.DOI, FileSHA256: stored.File.SHA256,
		StorageKey: stored.File.Key, Status: types.DocumentStatusUploaded,
	}
	created, err := s.repo.Create(ctx, document)
	if err != nil {
		_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		return nil, err
	}
	if s.papers != nil {
		sourceURL := canonicalRemoteLandingURL(resolved, stored)
		source := types.PaperMetadataSourceManual
		if _, metadataErr := s.papers.UpdateMetadata(ctx, created.ID.String(), types.PaperMetadataUpdate{
			SourceURL: &sourceURL, MetadataSource: &source,
		}); metadataErr != nil {
			logger.Warn(ctx, "persist imported paper source URL failed", "document_id", created.ID, "error", metadataErr)
		}
	}
	processed, processErr := s.startCreatedDocumentProcessing(ctx, created)
	if processErr != nil {
		return processed, processErr
	}
	logger.Info(ctx, "remote document created", "document_id", processed.ID, "source_url", resolved.LandingURL)
	return processed, nil
}

// ImportRemoteDocument is the independently retryable worker for one resolved
// bibliography target. The payload contains no URL; current metadata is
// reloaded and resolved on every attempt.
func (s *documentService) ImportRemoteDocument(ctx context.Context, task *asynq.Task) (outputErr error) {
	if task == nil {
		return errors.New("remote document import task is required")
	}
	var payload types.DocumentRemoteImportPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode remote document import payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	documentID := uuid.MustParse(payload.DocumentID)
	ctx = logger.WithAttrs(ctx, "document_id", documentID, "task_type", task.Type())
	rootScopeID := remoteDocumentScopeID(documentID, payload.Attempt)
	if s.spans != nil {
		cancelled, err := s.spans.ScopeCancelled(ctx, documentID,
			types.ProcessingSpanReferenceDownload, types.ProcessingSpanScopeRemoteDocument, rootScopeID)
		if err != nil {
			return err
		}
		if cancelled {
			return fmt.Errorf("%w: %w", asynq.RevokeTask, types.ErrRemotePDFRunCancelled)
		}
	}
	var rootSpan uuid.UUID
	if s.spans != nil {
		rootSpan, _ = s.spans.Begin(ctx, documentID, positiveAttempt(payload.Attempt),
			types.ProcessingSpanReferenceDownload, types.ProcessingSpanScopeRemoteDocument, rootScopeID, retryCount(ctx))
		defer func() {
			if rootSpan == uuid.Nil {
				return
			}
			if outputErr != nil {
				code, reason := spanFailure(outputErr)
				_ = s.spans.Fail(context.WithoutCancel(ctx), rootSpan, code, reason)
			} else {
				_ = s.spans.Succeed(context.WithoutCancel(ctx), rootSpan)
			}
			if outputErr == nil || errors.Is(outputErr, asynq.SkipRetry) ||
				errors.Is(outputErr, asynq.RevokeTask) || finalAsynqAttempt(ctx) {
				_ = s.spans.FinalizeParent(context.WithoutCancel(ctx), rootSpan)
			}
		}()
	}

	document, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status != types.DocumentStatusUnknown {
		// A previous attempt or another worker already acquired the object.
		return nil
	}
	if s.papers == nil || s.urlResolver == nil {
		return errors.New("remote paper dependencies are not configured")
	}
	metadata, err := s.papers.GetMetadata(ctx, documentID.String())
	if err != nil {
		return err
	}

	resolved, err := s.resolveRemoteStage(ctx, rootSpan, document, *metadata, payload.Attempt)
	if err != nil {
		return terminalRemoteImportError(err)
	}
	if cancelled, err := s.remoteImportCancelled(ctx, documentID, payload.Attempt); err != nil {
		return err
	} else if cancelled {
		return fmt.Errorf("%w: %w", asynq.RevokeTask, types.ErrRemotePDFRunCancelled)
	}

	deduplicateSpan := s.beginRemoteChild(ctx, rootSpan, document.ID, payload.Attempt, types.ProcessingSpanDeduplicateDocument)
	if duplicate, err := s.findDuplicateDocument(ctx, documentID, resolved.DOI, ""); err != nil {
		s.finishRemoteChild(ctx, deduplicateSpan, err)
		return err
	} else if duplicate != uuid.Nil {
		redirectErr := s.redirectIncomingCitations(ctx, documentID, duplicate)
		s.finishRemoteChild(ctx, deduplicateSpan, redirectErr)
		return redirectErr
	}

	s.finishRemoteChild(ctx, deduplicateSpan, nil)
	stored, err := s.downloadRemoteStage(ctx, rootSpan, document, resolved, payload.Attempt)
	if err != nil {
		return terminalRemoteImportError(err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		}
	}()
	if cancelled, err := s.remoteImportCancelled(ctx, documentID, payload.Attempt); err != nil {
		return err
	} else if cancelled {
		return fmt.Errorf("%w: %w", asynq.RevokeTask, types.ErrRemotePDFRunCancelled)
	}

	deduplicateSpan = s.beginRemoteChild(ctx, rootSpan, document.ID, payload.Attempt, types.ProcessingSpanDeduplicateDocument)
	if duplicate, err := s.findDuplicateDocument(ctx, documentID, "", stored.File.SHA256); err != nil {
		s.finishRemoteChild(ctx, deduplicateSpan, err)
		return err
	} else if duplicate != uuid.Nil {
		if err := s.redirectIncomingCitations(ctx, documentID, duplicate); err != nil {
			s.finishRemoteChild(ctx, deduplicateSpan, err)
			return err
		}
		s.finishRemoteChild(ctx, deduplicateSpan, nil)
		return nil
	}

	promoted, err := s.repo.PromoteUnknown(ctx, types.Document{
		ID: documentID, OriginalFilename: stored.File.Filename,
		ContentType: stored.File.ContentType, SizeBytes: stored.File.Size,
		DOI: resolved.DOI, FileSHA256: stored.File.SHA256,
		StorageKey: stored.File.Key,
	})

	if errors.Is(err, types.ErrDuplicateDOI) || errors.Is(err, types.ErrDuplicateFileHash) {
		duplicate, searchErr := s.findDuplicateDocument(ctx, documentID, resolved.DOI, stored.File.SHA256)
		if searchErr != nil {
			s.finishRemoteChild(ctx, deduplicateSpan, searchErr)
			return searchErr
		}
		if duplicate != uuid.Nil {
			redirectErr := s.redirectIncomingCitations(ctx, documentID, duplicate)
			s.finishRemoteChild(ctx, deduplicateSpan, redirectErr)
			return redirectErr
		}
	}
	if err != nil {
		s.finishRemoteChild(ctx, deduplicateSpan, err)
		return err
	}
	if !promoted {
		s.finishRemoteChild(ctx, deduplicateSpan, nil)
		return nil
	}
	s.finishRemoteChild(ctx, deduplicateSpan, nil)
	cleanup = false

	if sourceURL := canonicalRemoteLandingURL(resolved, stored); sourceURL != "" {
		source := types.PaperMetadataSourceCatalog
		_, _ = s.papers.UpdateMetadata(ctx, documentID.String(), types.PaperMetadataUpdate{SourceURL: &sourceURL, MetadataSource: &source})
	}

	enqueueSpan := s.beginRemoteChild(ctx, rootSpan, document.ID, payload.Attempt, types.ProcessingSpanEnqueueDocumentProcess)
	if err := s.enqueueDocumentRecognition(ctx, documentID); err != nil {
		s.finishRemoteChild(ctx, enqueueSpan, err)
		return err
	}
	s.finishRemoteChild(ctx, enqueueSpan, nil)
	logger.Info(ctx, "remote reference document acquired", "document_id", documentID, "storage_key", stored.File.Key)
	return nil
}

func (s *documentService) resolveRemoteStage(ctx context.Context, parent uuid.UUID, document types.Document, metadata types.PaperMetadata, attempt int) (types.ResolvedPaperURL, error) {
	span := s.beginRemoteChild(ctx, parent, document.ID, attempt, types.ProcessingSpanResolvePDFURL)
	resolved, err := s.urlResolver.Resolve(ctx, types.PaperURLResolveRequest{SourceURL: metadata.SourceURL, ExplicitDOI: document.DOI})
	s.finishRemoteChild(ctx, span, err)
	return resolved, err
}

func (s *documentService) downloadRemoteStage(ctx context.Context, parent uuid.UUID, document types.Document, resolved types.ResolvedPaperURL, attempt int) (interfaces.SaveFromURLResult, error) {
	span := s.beginRemoteChild(ctx, parent, document.ID, attempt, types.ProcessingSpanDownloadPDF)
	result, err := s.downloadResolvedPDF(ctx, resolved.DownloadURL, resolved.Filename)
	s.finishRemoteChild(ctx, span, err)
	return result, err
}

func (s *documentService) downloadResolvedPDF(ctx context.Context, downloadURL, filename string) (interfaces.SaveFromURLResult, error) {
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return interfaces.SaveFromURLResult{}, err
	}
	result, err := s.storage.SaveFromURL(ctx, interfaces.SaveFromURLRequest{URL: downloadURL, Filename: filename, Category: interfaces.StorageCategoryPDF})
	var remoteErr *fileservice.RemoteHTTPError
	if errors.As(err, &remoteErr) && remoteErr.RetryAfter > 0 && s.downloads != nil {
		host := parsed.Hostname()
		if finalURL, parseErr := url.Parse(remoteErr.FinalURL); parseErr == nil && finalURL.Hostname() != "" {
			host = finalURL.Hostname()
		}
		_ = s.downloads.Cooldown(context.WithoutCancel(ctx), host, remoteErr.RetryAfter)
	}
	return result, err
}

func canonicalRemoteLandingURL(
	resolved types.ResolvedPaperURL,
	stored interfaces.SaveFromURLResult,
) string {
	landing := strings.TrimSpace(resolved.LandingURL)
	// Known providers deliberately keep a human landing page separate from the
	// byte URL. For a generic direct-PDF import, the final validated redirect
	// target is the best canonical source URL available.
	if landing == strings.TrimSpace(resolved.DownloadURL) && strings.TrimSpace(stored.FinalURL) != "" {
		return strings.TrimSpace(stored.FinalURL)
	}
	return landing
}

func (s *documentService) findDuplicateDocument(ctx context.Context, exclude uuid.UUID, doi, hash string) (uuid.UUID, error) {
	for _, search := range []types.DocumentSearch{{DOI: types.NormalizeDOI(doi), Page: 1, PageSize: 2}, {FileSHA256: hash, Page: 1, PageSize: 2}} {
		if search.DOI == "" && search.FileSHA256 == "" {
			continue
		}
		page, err := s.repo.Search(ctx, search)
		if err != nil {
			return uuid.Nil, err
		}
		for _, item := range page.Items {
			if item.ID != exclude && item.Status != types.DocumentStatusUnknown {
				return item.ID, nil
			}
		}
	}
	return uuid.Nil, nil
}

func (s *documentService) redirectIncomingCitations(ctx context.Context, placeholderID, canonicalID uuid.UUID) error {
	incoming, err := s.papers.ListCitedBy(ctx, placeholderID.String())
	if err != nil {
		return err
	}
	for _, citation := range incoming {
		if _, err := s.papers.ResolveCitation(ctx, citation.CitingDocumentID.String(), canonicalID.String(), citation.RawReference); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	_, _ = s.repo.DeleteUnknownIfUnreferenced(ctx, placeholderID)
	logger.Info(ctx, "remote duplicate citations redirected", "placeholder_document_id", placeholderID, "canonical_document_id", canonicalID)
	return nil
}

func terminalRemoteImportError(err error) error {
	if errors.Is(err, types.ErrRemotePDFUnavailable) || errors.Is(err, fileservice.ErrInvalidPDF) || errors.Is(err, fileservice.ErrUnsupportedFileType) {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	var remoteErr *fileservice.RemoteHTTPError
	if errors.As(err, &remoteErr) && !remoteErr.TaskRetryable() {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}
	return err
}

func (s *documentService) beginRemoteChild(ctx context.Context, parent, documentID uuid.UUID, attempt int, kind types.ProcessingSpanKind) uuid.UUID {
	if s.spans == nil {
		return uuid.Nil
	}
	scopeID := types.ComposeProcessingSpanScopeID(kind, documentID.String())
	var id uuid.UUID
	if parent != uuid.Nil {
		id, _ = s.spans.BeginChild(ctx, parent, documentID, positiveAttempt(attempt), kind, types.ProcessingSpanScopeRemoteDocument, scopeID, retryCount(ctx))
	} else {
		id, _ = s.spans.Begin(ctx, documentID, positiveAttempt(attempt), kind, types.ProcessingSpanScopeRemoteDocument, scopeID, retryCount(ctx))
	}
	return id
}

func (s *documentService) finishRemoteChild(ctx context.Context, spanID uuid.UUID, err error) {
	if s.spans == nil || spanID == uuid.Nil {
		return
	}
	if err == nil {
		_ = s.spans.Succeed(context.WithoutCancel(ctx), spanID)
		return
	}
	code, reason := spanFailure(err)
	_ = s.spans.Fail(context.WithoutCancel(ctx), spanID, code, reason)
}

func (s *documentService) remoteImportCancelled(
	ctx context.Context, documentID uuid.UUID, attempt int,
) (bool, error) {
	if s.spans == nil {
		return false, nil
	}
	return s.spans.ScopeCancelled(ctx, documentID,
		types.ProcessingSpanReferenceDownload, types.ProcessingSpanScopeRemoteDocument,
		remoteDocumentScopeID(documentID, attempt))
}

func positiveAttempt(value int) int {
	if value > 0 {
		return value
	}
	return 1
}
func retryCount(ctx context.Context) int { value, _ := asynq.GetRetryCount(ctx); return value }

func finalAsynqAttempt(ctx context.Context) bool {
	retry, retryOK := asynq.GetRetryCount(ctx)
	maximum, maxOK := asynq.GetMaxRetry(ctx)
	return retryOK && maxOK && retry >= maximum
}
