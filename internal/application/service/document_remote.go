package service

import (
	"context"
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
		existing, getErr := s.repo.Get(ctx, duplicate)
		if getErr != nil {
			return nil, getErr
		}
		return &existing, ErrDuplicateDOI
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
		existing, getErr := s.repo.Get(ctx, duplicate)
		if getErr != nil {
			return nil, getErr
		}
		return &existing, ErrDuplicateFileHash
	}
	document := types.Document{
		ID: uuid.New(), OriginalFilename: stored.File.Filename,
		ContentType: stored.File.ContentType, SizeBytes: stored.File.Size,
		DOI: resolved.DOI, FileSHA256: stored.File.SHA256,
		StorageKey:  stored.File.Key,
		SourceState: types.DocumentSourceAvailable,
	}
	created, err := s.repo.Create(ctx, document)
	if err != nil {
		_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		if errors.Is(err, types.ErrDuplicateFileHash) {
			if existing, lookupErr := s.existingDocument(ctx, "", stored.File.SHA256); lookupErr == nil && existing != nil {
				return existing, err
			}
		}
		if errors.Is(err, types.ErrDuplicateDOI) {
			if existing, lookupErr := s.existingDocument(ctx, resolved.DOI, ""); lookupErr == nil && existing != nil {
				return existing, err
			}
		}
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

// RequestReferencePDFDownload queues a download only after an explicit user
// action on one unresolved bibliography target. Parsing and catalog lookup do
// not call this method.
func (s *documentService) RequestReferencePDFDownload(ctx context.Context, id string) error {
	documentID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil || documentID == uuid.Nil {
		return errors.New("invalid document id")
	}
	document, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState != types.DocumentSourceMissing {
		return errors.New("reference already has a source document")
	}
	if s.papers == nil || s.taskEnqueuer == nil {
		return errors.New("reference download is not configured")
	}
	metadata, err := s.papers.GetMetadata(ctx, documentID.String())
	if err != nil {
		return err
	}
	if strings.TrimSpace(metadata.SourceURL) == "" {
		return types.ErrRemotePDFUnavailable
	}
	generation := document.SourceLookupGeneration
	if generation < 1 {
		generation = 1
	}
	task, options, err := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{
		DocumentID: documentID.String(), Generation: generation,
	})
	if err != nil {
		return err
	}
	if err := enqueueManagedTask(ctx, s.taskEnqueuer, task, options, documentID.String()); err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
		return err
	}
	logger.Info(ctx, "reference PDF download queued by user", "document_id", documentID)
	return nil
}

// ImportRemoteDocument executes only an explicit reference-download task.
func (s *documentService) ImportRemoteDocument(ctx context.Context, task *asynq.Task) error {
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
	document, err := s.repo.Get(ctx, documentID)
	if err != nil || document.SourceState != types.DocumentSourceMissing {
		return err
	}
	if s.papers == nil || s.urlResolver == nil {
		return errors.New("remote paper dependencies are not configured")
	}
	metadata, err := s.papers.GetMetadata(ctx, documentID.String())
	if err != nil {
		return err
	}
	resolved, err := s.urlResolver.Resolve(ctx, types.PaperURLResolveRequest{SourceURL: metadata.SourceURL, ExplicitDOI: document.DOI})
	if err != nil {
		return terminalRemoteImportError(err)
	}
	if duplicate, err := s.findDuplicateDocument(ctx, documentID, resolved.DOI, ""); err != nil {
		return err
	} else if duplicate != uuid.Nil {
		return s.redirectIncomingCitations(ctx, documentID, duplicate)
	}
	stored, err := s.downloadResolvedPDF(ctx, resolved.DownloadURL, resolved.Filename)
	if err != nil {
		return terminalRemoteImportError(err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		}
	}()
	if duplicate, err := s.findDuplicateDocument(ctx, documentID, "", stored.File.SHA256); err != nil {
		return err
	} else if duplicate != uuid.Nil {
		return s.redirectIncomingCitations(ctx, documentID, duplicate)
	}
	promoted, err := s.repo.PromoteUnknown(ctx, types.Document{ID: documentID, OriginalFilename: stored.File.Filename, ContentType: stored.File.ContentType, SizeBytes: stored.File.Size, DOI: resolved.DOI, FileSHA256: stored.File.SHA256, StorageKey: stored.File.Key, SourceState: types.DocumentSourceAvailable})
	if err != nil || !promoted {
		return err
	}
	cleanup = false
	if sourceURL := canonicalRemoteLandingURL(resolved, stored); sourceURL != "" {
		source := types.PaperMetadataSourceCatalog
		_, _ = s.papers.UpdateMetadata(ctx, documentID.String(), types.PaperMetadataUpdate{SourceURL: &sourceURL, MetadataSource: &source})
	}
	return s.enqueueDocumentRecognition(ctx, documentID)
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
			return uuid.Nil, fmt.Errorf("%w: %w", errDocumentDuplicateLookup, err)
		}
		for _, item := range page.Items {
			if item.ID != exclude && item.SourceState == types.DocumentSourceAvailable {
				return item.ID, nil
			}
		}
	}
	return uuid.Nil, nil
}

func (s *documentService) redirectIncomingCitations(ctx context.Context, placeholderID, canonicalID uuid.UUID) error {
	if err := s.repo.MergeIntoCanonical(ctx, placeholderID, canonicalID); err != nil {
		return err
	}
	logger.Info(ctx, "reference document merged into canonical", "placeholder_document_id", placeholderID, "canonical_document_id", canonicalID)
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

func positiveAttempt(value int) int {
	if value > 0 {
		return value
	}
	return 1
}
func finalAsynqAttempt(ctx context.Context) bool {
	retry, retryOK := asynq.GetRetryCount(ctx)
	maximum, maxOK := asynq.GetMaxRetry(ctx)
	return retryOK && maxOK && retry >= maximum
}
