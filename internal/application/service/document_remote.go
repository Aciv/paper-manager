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

// ErrReferenceAlreadyResolved prevents a second PDF import for a reference
// that already points at a stored document.
var ErrReferenceAlreadyResolved = errors.New("reference already resolved to a document")

// RequestReferencePDFDownload queues a download only after an explicit user
// action on one unresolved reference. Parsing and catalog lookup never call it.
func (s *documentService) RequestReferencePDFDownload(ctx context.Context, referenceID string) error {
	id, err := uuid.Parse(strings.TrimSpace(referenceID))
	if err != nil || id == uuid.Nil {
		return errors.New("invalid reference id")
	}
	if s.papers == nil || s.taskEnqueuer == nil {
		return errors.New("reference download is not configured")
	}
	reference, err := s.papers.GetReference(ctx, id.String())
	if err != nil {
		return err
	}
	if reference.CitedDocumentID != nil {
		return ErrReferenceAlreadyResolved
	}
	if referenceSourceURL(*reference) == "" {
		return types.ErrRemotePDFUnavailable
	}
	generation := reference.ResolutionGeneration
	if generation < 1 {
		generation = 1
	}
	task, options, err := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{
		ReferenceID: id.String(), Generation: generation,
	})
	if err != nil {
		return err
	}
	if err := enqueueManagedTask(ctx, s.taskEnqueuer, task, options, id.String()); err != nil {
		return err
	}
	logger.Info(ctx, "reference PDF download queued by user", "reference_id", id)
	return nil
}

// ImportRemoteDocument executes only an explicit reference-download task.
func (s *documentService) ImportRemoteDocument(ctx context.Context, task *asynq.Task) error {
	if task == nil {
		return errors.New("remote reference import task is required")
	}
	var payload types.DocumentRemoteImportPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("decode remote reference import payload: %w", err)
	}
	if err := payload.Validate(); err != nil {
		return err
	}
	if payload.ReferenceID == "" {
		return fmt.Errorf("%w: legacy document remote import is retired", asynq.SkipRetry)
	}
	return s.importReferencePDF(ctx, payload.ReferenceID)
}

func (s *documentService) importReferencePDF(ctx context.Context, referenceID string) error {
	id, err := uuid.Parse(strings.TrimSpace(referenceID))
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("invalid reference id: %w", asynq.SkipRetry)
	}
	if s.papers == nil || s.urlResolver == nil {
		return errors.New("remote paper dependencies are not configured")
	}
	reference, err := s.papers.GetReference(ctx, id.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: reference not found", asynq.SkipRetry)
		}
		return err
	}
	if reference.CitedDocumentID != nil {
		return s.ensureReferenceRecognition(ctx, *reference.CitedDocumentID)
	}
	sourceURL := referenceSourceURL(*reference)
	if sourceURL == "" {
		return terminalRemoteImportError(types.ErrRemotePDFUnavailable)
	}
	resolved, err := s.urlResolver.Resolve(ctx, types.PaperURLResolveRequest{
		SourceURL: sourceURL, ExplicitDOI: reference.DOI,
	})
	if err != nil {
		return terminalRemoteImportError(err)
	}
	doi := resolved.DOI
	if doi == "" {
		doi = reference.DOI
	}
	if duplicate, err := s.findDuplicateDocument(ctx, reference.CitingDocumentID, doi, ""); err != nil {
		return err
	} else if duplicate != uuid.Nil {
		return s.attachReferenceToDocument(ctx, id, duplicate)
	}
	stored, err := s.downloadResolvedPDF(ctx, resolved.DownloadURL, resolved.Filename)
	if err != nil {
		return terminalRemoteImportError(err)
	}
	cleanupFile := true
	defer func() {
		if cleanupFile {
			_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
		}
	}()
	if duplicate, err := s.findDuplicateDocument(ctx, reference.CitingDocumentID, "", stored.File.SHA256); err != nil {
		return err
	} else if duplicate != uuid.Nil {
		return s.attachReferenceToDocument(ctx, id, duplicate)
	}

	documentID := uuid.New()
	created, err := s.repo.Create(ctx, types.Document{
		ID:               documentID,
		OriginalFilename: stored.File.Filename,
		ContentType:      stored.File.ContentType,
		SizeBytes:        stored.File.Size,
		DOI:              doi,
		FileSHA256:       stored.File.SHA256,
		StorageKey:       stored.File.Key,
		SourceState:      types.DocumentSourceAvailable,
	})
	if err != nil {
		if errors.Is(err, types.ErrDuplicateDOI) || errors.Is(err, types.ErrDuplicateFileHash) {
			if duplicate, lookupErr := s.findDuplicateDocument(ctx, reference.CitingDocumentID, doi, stored.File.SHA256); lookupErr != nil {
				return lookupErr
			} else if duplicate != uuid.Nil {
				return s.attachReferenceToDocument(ctx, id, duplicate)
			}
		}
		return err
	}
	cleanupDocument := func() {
		_ = s.repo.Delete(context.WithoutCancel(ctx), created.ID)
		_ = s.storage.Delete(context.WithoutCancel(ctx), stored.File.Key)
	}
	if _, err := s.papers.UpdateMetadata(ctx, created.ID.String(), referenceDocumentMetadata(*reference, resolved, stored)); err != nil {
		cleanupDocument()
		return fmt.Errorf("persist imported reference metadata: %w", err)
	}
	linked, err := s.papers.LinkReferenceDocument(ctx, id, created.ID, false)
	if err != nil {
		cleanupDocument()
		return err
	}
	if linked.CitedDocumentID == nil || *linked.CitedDocumentID != created.ID {
		cleanupDocument()
		if linked.CitedDocumentID != nil {
			return s.ensureReferenceRecognition(ctx, *linked.CitedDocumentID)
		}
		return errors.New("reference link did not attach the imported document")
	}
	cleanupFile = false
	if err := s.enqueueDocumentRecognition(ctx, created.ID); err != nil {
		return err
	}
	logger.Info(ctx, "reference PDF imported",
		"reference_id", id, "document_id", created.ID, "source_url", resolved.LandingURL)
	return nil
}

func (s *documentService) attachReferenceToDocument(
	ctx context.Context, referenceID, documentID uuid.UUID,
) error {
	linked, err := s.papers.LinkReferenceDocument(ctx, referenceID, documentID, false)
	if err != nil {
		return err
	}
	if linked.CitedDocumentID != nil && *linked.CitedDocumentID == documentID {
		return s.ensureReferenceRecognition(ctx, documentID)
	}
	if linked.CitedDocumentID != nil {
		return s.ensureReferenceRecognition(ctx, *linked.CitedDocumentID)
	}
	return errors.New("reference could not be attached to an existing document")
}

func (s *documentService) ensureReferenceRecognition(ctx context.Context, documentID uuid.UUID) error {
	stage, err := s.repo.GetStage(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil {
		return err
	}
	if stage.Status.Active() || stage.Status == types.DocumentStageSucceeded {
		return nil
	}
	return s.enqueueDocumentRecognition(ctx, documentID)
}

func referenceSourceURL(reference types.PaperReference) string {
	if value := strings.TrimSpace(reference.SourceURL); value != "" {
		return value
	}
	return strings.TrimSpace(referenceLookupQuery(reference.RawReference).SourceURL)
}

func referenceDocumentMetadata(
	reference types.PaperReference,
	resolved types.ResolvedPaperURL,
	stored interfaces.SaveFromURLResult,
) types.PaperMetadataUpdate {
	source := types.PaperMetadataSourceCatalog
	update := types.PaperMetadataUpdate{MetadataSource: &source}
	if title := strings.TrimSpace(reference.Title); title != "" {
		update.Title = &title
	}
	if len(reference.Authors) > 0 {
		authors := reference.Authors
		update.Authors = &authors
	}
	if venue := strings.TrimSpace(reference.Venue); venue != "" {
		update.Venue = &venue
	}
	if reference.PublishedAt != nil {
		update.PublishedAt = reference.PublishedAt
	}
	if volume := strings.TrimSpace(reference.Volume); volume != "" {
		update.Volume = &volume
	}
	if issue := strings.TrimSpace(reference.Issue); issue != "" {
		update.Issue = &issue
	}
	if pages := strings.TrimSpace(reference.Pages); pages != "" {
		update.Pages = &pages
	}
	if sourceURL := canonicalRemoteLandingURL(resolved, stored); sourceURL != "" {
		update.SourceURL = &sourceURL
	} else if value := referenceSourceURL(reference); value != "" {
		update.SourceURL = &value
	}
	return update
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
