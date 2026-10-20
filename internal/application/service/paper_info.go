package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

const (
	minPaperCatalogMatchScore             = 0.72
	paperStrongTitleMatchScore            = 0.85
	paperWeakTitleMatchScore              = 0.60
	paperWeakTitleAuthorMatchScore        = 0.60
	paperStrongTitleWeight                = 0.95
	paperWeakTitleWeight                  = 0.75
	paperWeakTitleAuthorWeight            = 0.20
	paperProviderScoreWeight              = 0.05
	maxAutomaticPaperTags                 = 32
	localPaperTitleSearchSize             = 50
	defaultReferenceCatalogMaxPerDocument = 25
)

var (
	// ErrStalePaperInfo means the source document was deleted or changed state
	// while paper-info work was running.
	ErrStalePaperInfo = errors.New("paper info source state is stale")
	// ErrPaperCatalogNoMatch means providers were reachable but no candidate
	// was sufficiently similar to safely overwrite canonical paper data.
	ErrPaperCatalogNoMatch = errors.New("no satisfactory paper catalog match")
	// ErrPaperInfoExtractionUnavailable means there is no parsed Markdown to
	// submit to the Python extractor.
	ErrPaperInfoExtractionUnavailable = errors.New("paper metadata extraction is unavailable")
	// ErrPaperInfoCatalogUnavailable means durable metadata/reference evidence
	// cannot form a safe external catalog query.
	ErrPaperInfoCatalogUnavailable = errors.New("paper catalog resolution is unavailable")
	// ErrPaperInfoDeleting prevents a manual retry from racing deletion.
	ErrPaperInfoDeleting = errors.New("document is being deleted")
	// ErrPaperInfoAttemptActive prevents a retry from overlapping queued or
	// running work for the same durable document stage.
	ErrPaperInfoAttemptActive = errors.New("paper information attempt is still active")
	// errDuplicateReplacementRaced means concurrent uploads kept replacing the
	// DOI owner while newest-wins reconciliation ran. It is retryable so the
	// task re-reads the surviving document instead of revoking with a blank DOI.
	errDuplicateReplacementRaced = errors.New("duplicate document replacement raced")
)

var (
	// (?i) capitalization ignore, \b word boundary, afrond isn't digit...
	// {4,9} 4 to 9 digit
	doiPattern       = regexp.MustCompile(`(?i)\b10\.[0-9]{4,9}/[-._;()/:a-z0-9]+`)
	urlPattern       = regexp.MustCompile(`(?i)https?://[^\s<>()]+`)
	yearTitlePattern = regexp.MustCompile(
		`(?i)\b(?:19|20)[0-9]{2}[a-z]?\s*\.\s+(.+?)(?:\.\s+|$)`,
	)
	arxivIDPattern = regexp.MustCompile(
		`(?i)(?:arxiv\s*:\s*|arxiv\.org/(?:abs|pdf)/|10\.48550/arxiv\.)([a-z-]+(?:\.[a-z-]+)?/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(?:v[0-9]+)?`,
	)
	// arxivBareIDPattern accepts a parser-provided ID without a URL or
	// "arXiv:" prefix. The capture intentionally excludes its optional
	// version so the persisted landing URL is stable across versions.
	arxivBareIDPattern = regexp.MustCompile(
		`(?i)^([a-z-]+(?:\.[a-z-]+)?/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(?:v[0-9]+)?$`,
	)
)

// paperInfoService coordinates durable document state, Python extraction,
// external catalogs, and the canonical PaperService boundary.
type paperInfoService struct {
	cfg        config.Config
	documents  interfaces.DocumentRepository
	storage    interfaces.FileStorage
	extractor  interfaces.PaperMetadataExtractor
	papers     interfaces.PaperService
	tasks      interfaces.TaskEnqueuer
	inspector  interfaces.TaskInspector
	dispatcher interfaces.CatalogDispatcher
	spans      interfaces.SpanRecorder
}

// NewPaperInfoService creates the application-level paper information
// orchestrator. The in-process dispatcher owns provider allocation; queue
// decoding and retry policy remain in the router layer.
func NewPaperInfoService(
	cfg config.Config,
	documents interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor,
	papers interfaces.PaperService,
	tasks interfaces.TaskEnqueuer,
	inspector interfaces.TaskInspector,
	dispatcher interfaces.CatalogDispatcher,
) interfaces.PaperInfoService {
	return newPaperInfoService(cfg, documents, storage, extractor, papers, tasks, inspector, dispatcher, nil)
}

// NewPaperInfoServiceWithSpanRecorder is the production DI constructor with
// persistent processing-span support.
func NewPaperInfoServiceWithSpanRecorder(
	cfg config.Config,
	documents interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor,
	papers interfaces.PaperService,
	tasks interfaces.TaskEnqueuer,
	inspector interfaces.TaskInspector,
	dispatcher interfaces.CatalogDispatcher,
	spans interfaces.SpanRecorder,
) interfaces.PaperInfoService {
	return newPaperInfoService(cfg, documents, storage, extractor, papers, tasks, inspector, dispatcher, spans)
}

func newPaperInfoService(
	cfg config.Config, documents interfaces.DocumentRepository, storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor,
	papers interfaces.PaperService, tasks interfaces.TaskEnqueuer, inspector interfaces.TaskInspector,
	dispatcher interfaces.CatalogDispatcher,
	spans interfaces.SpanRecorder,
) interfaces.PaperInfoService {
	return &paperInfoService{
		cfg:        cfg,
		documents:  documents,
		storage:    storage,
		extractor:  extractor,
		papers:     papers,
		tasks:      tasks,
		inspector:  inspector,
		dispatcher: dispatcher,
		spans:      spans,
	}
}

// ExtractMetadata loads the current parsed Markdown, asks
// Python for bibliographic candidates, and replaces the current extraction
// snapshot. Document-state checks are repeated immediately before each write.
func (s *paperInfoService) ExtractMetadata(
	ctx context.Context,
	command types.PaperMetadataExtractCommand,
) (catalogResolvable bool, outputErr error) {
	if err := command.Validate(); err != nil {
		return false, err
	}
	_, err := startDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation)
	if err != nil {
		return false, err
	}
	defer func() {
		finishDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation, outputErr)
	}()
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	logger.Info(ctx, "paper metadata extraction started")
	var spanID uuid.UUID
	if s.spans != nil {
		var spanErr error
		spanID, spanErr = s.spans.BeginSpan(ctx, command.DocumentID, stageAttempt(command.Generation, 0), types.ProcessingSpanMetadataExtract, types.ProcessingSpanScopeDocument, types.ComposeProcessingSpanScopeID(types.ProcessingSpanMetadataExtract, command.DocumentID.String()), 0)
		if spanErr != nil {
			logger.Warn(ctx, "start metadata extraction span failed", "error", spanErr)
		}
		defer func() {
			if outputErr != nil {
				code, reason := spanFailure(outputErr)
				_ = s.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanFailed, code, reason)
			} else {
				_ = s.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanSucceeded, "", "")
			}
		}()
	}
	// running report
	stopHeartbeat := keepSpanAlive(ctx, s.spans, spanID)
	defer stopHeartbeat()

	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return false, err
	}
	markdown, blocks, sourceStorageKey, err := s.loadExtractionSource(ctx, command)
	if err != nil {
		logger.Error(ctx, "paper metadata extraction source unavailable", "error", err)
		return false, err
	}
	result, err := s.extractor.Extract(ctx, &types.PaperExtractionRequest{
		DocumentID:       command.DocumentID,
		Markdown:         markdown,
		Blocks:           blocks,
		SourceStorageKey: sourceStorageKey,
	})
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, errors.New("paper metadata extractor returned nil result")
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return false, err
	}
	if err := result.Validate(); err != nil {
		return false, err
	}

	update := extractionMetadataUpdate(result)
	references := extractionCitationInputs(result.References, result.ReferenceCandidates)
	if err := s.ensureDocumentReady(ctx, command.DocumentID); err != nil {
		return false, err
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return false, err
	}
	var updatedMetadata *types.PaperMetadata
	if update.HasDataChanges() {
		updatedMetadata, err = s.papers.UpdateMetadata(
			ctx,
			command.DocumentID.String(),
			update,
		)
		if err != nil {
			logger.Error(ctx, "persist extracted paper metadata failed", "error", err)
			return false, fmt.Errorf("persist extracted paper metadata: %w", err)
		}
	} else {
		// An extractor that finds no usable field did not ask to clear the
		// canonical record. Reload it so catalog eligibility remains based on
		// persisted data rather than an empty transient result.
		updatedMetadata, err = s.papers.GetMetadata(ctx, command.DocumentID.String())
		if err != nil {
			return false, fmt.Errorf("load current paper metadata: %w", err)
		}
		logger.Info(ctx, "paper metadata extraction produced no usable fields")
	}
	if err := s.ensureDocumentReady(ctx, command.DocumentID); err != nil {
		return false, err
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return false, err
	}
	if result.ReferencesDetected {
		if _, err := s.papers.MergeExtractedCitations(
			ctx,
			command.DocumentID.String(),
			references,
		); err != nil {
			logger.Error(ctx, "persist extracted paper references failed", "error", err)
			return false, fmt.Errorf("persist extracted paper references: %w", err)
		}
	} else {
		logger.Info(ctx, "paper bibliography not detected; existing citations retained")
	}
	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return false, err
	}
	catalogResolvable = hasUsablePaperLookupIdentity(document.DOI, updatedMetadata.Title)
	logger.Info(ctx, "paper metadata extraction persisted",
		"author_count", len(result.Authors),
		"reference_count", len(references),
		"references_detected", result.ReferencesDetected,
		"metadata_applied", updatedMetadata.MetadataSource == types.PaperMetadataSourceExtraction,
		"catalog_resolvable", catalogResolvable,
		"extractor", result.ExtractorName)
	queueSourceLookup := s.enqueueSourceLookupIfNeeded
	if command.RestartSourceLookup {
		queueSourceLookup = s.restartSourceLookup
	}
	if err := queueSourceLookup(ctx, command.DocumentID, command.RequestID); err != nil {
		logger.Warn(ctx, "queue source lookup after extraction failed", "document_id", command.DocumentID, "error", err)
	}
	if s.cfg.PaperReferenceAutoResolve {
		if err := s.enqueueReferenceLookupIfNeeded(ctx, command.DocumentID, command.RequestID); err != nil {
			logger.Warn(ctx, "queue reference lookup after extraction failed", "document_id", command.DocumentID, "error", err)
		}
	}
	return catalogResolvable, nil
}

func hasUsablePaperLookupIdentity(doi, title string) bool {
	return strings.TrimSpace(doi) != "" || strings.TrimSpace(title) != ""
}

// PaperSourceQueryEnqueue starts one source-paper catalog lookup and hands it to the
// in-process dispatcher. The task never carries provider assignment or
// bibliography batches.
func (s *paperInfoService) PaperSourceQueryEnqueue(
	ctx context.Context,
	command types.PaperSourceQueryEnqueueCommand,
) (outputErr error) {
	if err := command.Validate(); err != nil {
		return err
	}
	if s.dispatcher == nil {
		return ErrPaperInfoCatalogUnavailable
	}
	_, err := startDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation)
	if err != nil {
		return err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			finishDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation, outputErr)
		}
	}()
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	ctx = logger.WithAttrs(ctx, "catalog_scope", "source")
	logger.Info(ctx, "paper catalog resolution started")
	var spanID uuid.UUID
	if s.spans != nil {
		var spanErr error
		spanID, spanErr = s.spans.BeginSpan(ctx, command.DocumentID, stageAttempt(command.Generation, 0), types.ProcessingSpanCatalogSource, types.ProcessingSpanScopeCatalogSource, types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogSource, command.DocumentID.String()), 0)
		if spanErr != nil {
			logger.Warn(ctx, "start catalog resolution span failed", "error", spanErr)
		}
		defer func() {
			if outputErr != nil {
				code, reason := spanFailure(outputErr)
				_ = s.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanFailed, code, reason)
			} else {
				_ = s.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanSucceeded, "", "")
			}
		}()
	}
	stopHeartbeat := keepSpanAlive(ctx, s.spans, spanID)
	defer stopHeartbeat()
	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return fmt.Errorf("%w: document is being deleted", ErrStalePaperInfo)
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return err
	}
	metadata, err := s.papers.GetMetadata(ctx, document.ID.String())
	if err != nil {
		return err
	}
	query := metadataLookupQuery(document, metadata)
	if err := query.Validate(); err != nil {
		logger.Info(ctx, "source catalog lookup skipped", "document_id", document.ID, "reason", "title and DOI are unavailable")
		return nil
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return err
	}
	if err := s.dispatcher.SubmitSource(ctx, types.CatalogSourceJob{
		RequestID: command.RequestID, DocumentID: document.ID,
		Generation: command.Generation, Query: query,
	}); err != nil {
		return fmt.Errorf("submit source catalog lookup: %w", err)
	}
	handedOff = true
	logger.Info(ctx, "source catalog lookup handed to dispatcher",
		"document_id", document.ID, "generation", command.Generation)
	return nil
}

func (s *paperInfoService) applySourceCatalogCandidate(
	ctx context.Context,
	document types.Document,
	selected types.PaperCatalogCandidate,
) (bool, error) {
	return s.applySourceCatalogCandidateUpdate(
		ctx, document, selected, catalogMetadataUpdate(selected),
	)
}

func (s *paperInfoService) mergeSourceCatalogCandidate(
	ctx context.Context,
	document types.Document,
	current *types.PaperMetadata,
	selected types.PaperCatalogCandidate,
) (bool, error) {
	return s.applySourceCatalogCandidateUpdate(
		ctx, document, selected, missingCatalogMetadataUpdate(current, selected),
	)
}

func (s *paperInfoService) applySourceCatalogCandidateUpdate(
	ctx context.Context,
	document types.Document,
	selected types.PaperCatalogCandidate,
	update types.PaperMetadataUpdate,
) (bool, error) {
	doi := normalizePaperDOI(selected.DOI)
	if strings.TrimSpace(document.DOI) == "" && doi != "" {
		kept, _, err := s.promoteNewestDuplicate(ctx, &document, doi)
		if err != nil {
			return false, err
		}
		if !kept {
			return false, nil
		}
	}
	if update.HasDataChanges() {
		updated, err := s.papers.UpdateMetadata(ctx, document.ID.String(), update)
		if err != nil {
			logger.Error(ctx, "persist selected paper catalog metadata failed",
				"provider", selected.Provider,
				"provider_id", selected.ProviderID,
				"error", err)
			return false, fmt.Errorf("persist selected catalog metadata: %w", err)
		}
		if updated.MetadataSource != types.PaperMetadataSourceCatalog {
			logger.Info(ctx, "paper catalog metadata skipped",
				"provider", selected.Provider,
				"provider_id", selected.ProviderID,
				"current_source", updated.MetadataSource)
			return false, nil
		}
	}
	if strings.TrimSpace(document.DOI) == "" && doi != "" {
		applied, doiErr := s.documents.SetDOIIfEmpty(ctx, document.ID, doi)
		if doiErr != nil {
			if errors.Is(doiErr, types.ErrDuplicateDOI) {
				kept, replaced, dedupeErr := s.promoteNewestDuplicate(ctx, &document, doi)
				if dedupeErr != nil {
					return false, dedupeErr
				}
				if !kept {
					return false, nil
				}
				if !replaced {
					return false, fmt.Errorf("backfill catalog DOI: %w", doiErr)
				}
				return true, nil
			}
			return false, fmt.Errorf("backfill catalog DOI: %w", doiErr)
		} else if applied {
			logger.Info(ctx, "catalog DOI applied to document", "document_id", document.ID, "doi", normalizePaperDOI(selected.DOI))
		}
	}
	return true, nil
}

const duplicateReplacementAttempts = 3

// promoteNewestDuplicate applies newest-wins duplicate handling when DOI
// already belongs to another available document. It keeps the document with
// the later creation time, deletes the older row and its merged tombstones,
// and returns whether the document under resolution survived.
func (s *paperInfoService) promoteNewestDuplicate(
	ctx context.Context,
	document *types.Document,
	doi string,
) (kept bool, replaced bool, err error) {
	if doi == "" || strings.TrimSpace(document.DOI) != "" {
		return true, false, nil
	}
	for attempt := 0; attempt < duplicateReplacementAttempts; attempt++ {
		if attempt > 0 {
			// A concurrent upload may have already deleted the document whose
			// source lookup is running. Then there is nothing left to keep.
			current, getErr := s.documents.Get(ctx, document.ID)
			if errors.Is(getErr, sql.ErrNoRows) {
				return false, true, nil
			} else if getErr != nil {
				return false, false, getErr
			}
			if current.SourceState != types.DocumentSourceAvailable {
				return false, true, nil
			}
		}
		page, searchErr := s.documents.Search(ctx, types.DocumentSearch{
			DOI: doi, SourceState: types.DocumentSourceAvailable, Page: 1, PageSize: 2,
		})
		if searchErr != nil {
			return false, false, searchErr
		}
		retry := false
		for _, existing := range page.Items {
			if existing.ID == document.ID {
				continue
			}
			newest, older := newestPaperDocument(*document, existing)
			cleanup, replaceErr := s.documents.ReplaceOlderDocument(ctx, older.ID, newest.ID)
			if replaceErr != nil {
				if errors.Is(replaceErr, sql.ErrNoRows) {
					// Another source-lookup task won the same race. Re-read the
					// DOI owner and compare again instead of revoking this task
					// with an unresolved DOI.
					retry = true
					break
				}
				return false, false, replaceErr
			}
			s.deleteReplacedDocumentFiles(ctx, cleanup)
			replaced = true
			if newest.ID != document.ID {
				logger.Info(ctx, "source document replaced by newer duplicate",
					"document_id", document.ID, "newest_document_id", newest.ID)
				return false, true, nil
			}
			document.DOI = doi
			logger.Info(ctx, "older duplicate replaced by source document",
				"document_id", document.ID, "older_document_id", older.ID)
		}
		if !retry {
			return true, replaced, nil
		}
	}
	return false, replaced, errDuplicateReplacementRaced
}

// newestPaperDocument picks the document to keep. A document with a real
// stored source always wins over one without; creation time decides only when
// both have a source. Ties favor the document currently being processed, which
// keeps retries deterministic.
func newestPaperDocument(current, existing types.Document) (newest, older types.Document) {
	currentHasSource := documentHasSource(current)
	existingHasSource := documentHasSource(existing)
	if currentHasSource != existingHasSource {
		if existingHasSource {
			return existing, current
		}
		return current, existing
	}
	if existing.CreatedAt.After(current.CreatedAt) {
		return existing, current
	}
	return current, existing
}

func documentHasSource(document types.Document) bool {
	return document.SourceState == types.DocumentSourceAvailable &&
		strings.TrimSpace(document.StorageKey) != ""
}

func (s *paperInfoService) deleteReplacedDocumentFiles(
	ctx context.Context, cleanup types.DocumentFileKeys,
) {
	if s.storage == nil {
		return
	}
	for _, key := range cleanup.StorageKeys {
		if err := s.storage.Delete(context.WithoutCancel(ctx), key); err != nil {
			logger.Warn(ctx, "delete replaced document file failed", "storage_key", key, "error", err)
		}
	}
}

func (s *paperInfoService) loadExtractionSource(
	ctx context.Context,
	command types.PaperMetadataExtractCommand,
) ([]byte, []types.PaperSourceBlock, string, error) {
	if err := s.ensureDocumentReady(ctx, command.DocumentID); err != nil {
		return nil, nil, "", err
	}
	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return nil, nil, "", err
	}
	manifest, err := s.documents.GetMarkdown(ctx, command.DocumentID)
	if err != nil {
		return nil, nil, "", err
	}
	reader, err := s.storage.Open(ctx, manifest.StorageKey)
	if err != nil {
		logger.Error(ctx, "open Markdown for paper extraction failed",
			"storage_key", manifest.StorageKey,
			"error", err)
		return nil, nil, "", err
	}
	defer reader.Close()
	markdown, err := io.ReadAll(io.LimitReader(
		reader,
		int64(types.MaxPaperExtractionMarkdownBytes)+1,
	))
	if err != nil {
		return nil, nil, "", fmt.Errorf("read Markdown for paper extraction: %w", err)
	}
	if len(markdown) > types.MaxPaperExtractionMarkdownBytes {
		return nil, nil, "", fmt.Errorf(
			"%w: Markdown exceeds %d bytes",
			types.ErrInvalidPaperExtraction,
			types.MaxPaperExtractionMarkdownBytes,
		)
	}
	persistedBlocks, err := s.documents.ListBlocks(ctx, command.DocumentID, 0)
	if err != nil {
		return nil, nil, "", err
	}
	blocks := make([]types.PaperSourceBlock, 0, len(persistedBlocks))
	for _, block := range persistedBlocks {
		blocks = append(blocks, types.PaperSourceBlock{
			Ordinal:  block.Ordinal,
			Type:     block.Type,
			Text:     block.Text,
			Location: block.Location,
		})
	}
	request := types.PaperExtractionRequest{
		DocumentID:       command.DocumentID,
		Markdown:         markdown,
		Blocks:           blocks,
		SourceStorageKey: document.StorageKey,
	}
	if err := request.Validate(); err != nil {
		return nil, nil, "", err
	}
	return markdown, blocks, document.StorageKey, nil
}

// ensureDocumentReady prevents late extraction results from being persisted
// after the source or Markdown stage has left the required state.
func (s *paperInfoService) ensureDocumentReady(ctx context.Context, documentID uuid.UUID) error {
	document, err := s.documents.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState != types.DocumentSourceAvailable || document.MarkdownStatus != types.DocumentStageSucceeded {
		return fmt.Errorf(
			"%w: source state is %s and Markdown stage is %s",
			ErrStalePaperInfo,
			document.SourceState,
			document.MarkdownStatus,
		)
	}
	return nil
}

func (s *paperInfoService) ensureCatalogSourceCurrent(
	ctx context.Context,
	initialDocument types.Document,
) error {
	current, err := s.documents.Get(ctx, initialDocument.ID)
	if err != nil {
		return err
	}
	if current.SourceState == types.DocumentSourceDeleting || current.SourceState == types.DocumentSourceMerged {
		return fmt.Errorf(
			"%w: document is being deleted",
			ErrStalePaperInfo,
		)
	}
	if !strings.EqualFold(
		strings.TrimSpace(initialDocument.DOI),
		strings.TrimSpace(current.DOI),
	) {
		return fmt.Errorf(
			"%w: document DOI changed during catalog lookup",
			ErrStalePaperInfo,
		)
	}
	return nil
}

func extractionMetadataUpdate(
	result *types.PaperExtractionResult,
) types.PaperMetadataUpdate {
	update := types.PaperMetadataUpdate{}
	if result.Title != nil {
		if title := compactPaperText(result.Title.Value); title != "" {
			update.Title = &title
		}
	}
	if result.Abstract != nil {
		if abstract := compactPaperText(result.Abstract.Value); abstract != "" {
			update.Abstract = &abstract
		}
	}
	authors := make([]types.PaperAuthor, 0, len(result.Authors))
	for _, candidate := range result.Authors {
		name := compactPaperText(candidate.Value)
		if name != "" {
			authors = append(authors, types.PaperAuthor{Name: name})
		}
	}
	if len(authors) > 0 {
		update.Authors = &authors
	}
	if update.HasDataChanges() {
		update.MetadataSource = paperMetadataSourcePointer(types.PaperMetadataSourceExtraction)
	}
	return update
}

func extractionCitationInputs(
	references []string,
	candidates []types.PaperReferenceCandidate,
) []types.PaperCitationInput {
	candidateByReference := make(map[string]types.PaperReferenceCandidate, len(candidates))
	for _, candidate := range candidates {
		candidate = compactPaperReferenceCandidate(candidate)
		key := compactPaperText(candidate.RawReference)
		if key == "" {
			continue
		}
		current, exists := candidateByReference[key]
		if !exists || candidate.Confidence > current.Confidence {
			candidateByReference[key] = candidate
		}
	}
	inputs := make([]types.PaperCitationInput, 0, len(references))
	seen := make(map[string]struct{}, len(references))
	for bibliographyOrder, rawReference := range references {
		rawReference = compactPaperText(rawReference)
		key := compactPaperText(rawReference)
		if rawReference == "" || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		order := bibliographyOrder
		input := types.PaperCitationInput{RawReference: rawReference, BibliographyOrder: &order}
		if candidate, exists := candidateByReference[key]; exists {
			candidate.RawReference = rawReference
			input.Candidate = &candidate
		}
		inputs = append(inputs, input)
	}
	return inputs
}

func compactPaperReferenceCandidate(candidate types.PaperReferenceCandidate) types.PaperReferenceCandidate {
	candidate.RawReference = compactPaperText(candidate.RawReference)
	candidate.Title = compactPaperText(candidate.Title)
	candidate.Venue = compactPaperText(candidate.Venue)
	candidate.DOI = compactPaperText(candidate.DOI)
	candidate.ArXivID = compactPaperText(candidate.ArXivID)
	candidate.Volume = compactPaperText(candidate.Volume)
	candidate.Pages = compactPaperText(candidate.Pages)
	authors := make([]string, 0, len(candidate.Authors))
	for _, author := range candidate.Authors {
		if author = compactPaperText(author); author != "" {
			authors = append(authors, author)
		}
	}
	candidate.Authors = authors
	return candidate
}

func metadataLookupQuery(
	document types.Document,
	metadata *types.PaperMetadata,
) types.PaperLookupQuery {
	query := types.PaperLookupQuery{
		DOI:   document.DOI,
		Limit: types.DefaultPaperCatalogLimit,
	}
	if metadata == nil {
		return query
	}
	query.Title = metadata.Title
	query.SourceURL = metadata.SourceURL
	query.Venue = metadata.Venue
	query.Authors = make([]string, 0, len(metadata.Authors))
	for _, author := range metadata.Authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			query.Authors = append(query.Authors, name)
		}
	}
	if metadata.PublishedAt != nil {
		query.PublishedYear = metadata.PublishedAt.Year()
	}
	return query
}

func scorePaperCatalogCandidate(
	query types.PaperLookupQuery,
	candidate types.PaperCatalogCandidate,
) (float64, bool) {
	queryDOI := normalizePaperDOI(query.DOI)
	candidateDOI := normalizePaperDOI(candidate.DOI)
	exactDOI := queryDOI != "" && candidateDOI != "" && queryDOI == candidateDOI
	queryArXivID := paperArXivID(query.DOI + " " + query.SourceURL)
	candidateArXivID := paperArXivID(
		candidate.DOI + " " + candidate.SourceURL + " " + candidate.ProviderID,
	)
	exactArXiv := queryArXivID != "" &&
		candidateArXivID != "" &&
		queryArXivID == candidateArXivID
	if exactDOI || exactArXiv {
		return 1, true
	}

	if strings.TrimSpace(query.Title) == "" {
		return 0, false
	}
	titleScore := paperTextSimilarity(query.Title, candidate.Title)
	providerScore := math.Max(0, math.Min(1, candidate.ProviderScore))
	if titleScore >= paperStrongTitleMatchScore {
		score := paperStrongTitleWeight*titleScore + paperProviderScoreWeight*providerScore
		return math.Max(0, math.Min(1, score)), false
	}
	// Author evidence is a guard only for an imperfect title. A bad parser
	// author must never veto a strong title, while a weak title must not match
	// merely because a common author name happens to overlap.
	if titleScore < paperWeakTitleMatchScore || len(query.Authors) == 0 {
		return 0, false
	}
	authorScore := paperAuthorSimilarity(query.Authors, candidate.Authors)
	if authorScore < paperWeakTitleAuthorMatchScore {
		return 0, false
	}
	score := paperWeakTitleWeight*titleScore +
		paperWeakTitleAuthorWeight*authorScore +
		paperProviderScoreWeight*providerScore
	return math.Max(0, math.Min(1, score)), false
}

func paperTextSimilarity(left, right string) float64 {
	leftTokens := paperTokens(left)
	rightTokens := paperTokens(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	intersection := 0
	for token := range leftTokens {
		if _, exists := rightTokens[token]; exists {
			intersection++
		}
	}
	union := len(leftTokens) + len(rightTokens) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func paperTokens(value string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(
		strings.ToLower(value),
		func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) },
	) {
		if token != "" {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func paperAuthorSimilarity(
	queryAuthors []string,
	candidateAuthors []types.PaperAuthor,
) float64 {
	if len(queryAuthors) == 0 || len(candidateAuthors) == 0 {
		return 0
	}
	candidateNames := make(map[string]struct{}, len(candidateAuthors)*2)
	for _, author := range candidateAuthors {
		name, surname := normalizedPaperAuthor(author.Name)
		if name != "" {
			candidateNames[name] = struct{}{}
		}
		if surname != "" {
			candidateNames[surname] = struct{}{}
		}
	}
	matches := 0
	for _, author := range queryAuthors {
		name, surname := normalizedPaperAuthor(author)
		if _, exists := candidateNames[name]; exists && name != "" {
			matches++
			continue
		}
		if _, exists := candidateNames[surname]; exists && surname != "" {
			matches++
		}
	}
	return math.Min(1, float64(matches)/float64(len(queryAuthors)))
}

func normalizedPaperAuthor(value string) (string, string) {
	parts := strings.FieldsFunc(
		strings.ToLower(value),
		func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) },
	)
	if len(parts) == 0 {
		return "", ""
	}
	return strings.Join(parts, " "), parts[len(parts)-1]
}

func normalizePaperDOI(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{
		"https://doi.org/",
		"http://doi.org/",
		"http://dx.doi.org/",
		"doi:",
	} {
		value = strings.TrimPrefix(value, prefix)
	}
	return strings.TrimRight(value, " \t\r\n.,;:")
}

func paperArXivID(value string) string {
	match := arxivIDPattern.FindStringSubmatch(value)
	if len(match) == 2 {
		return strings.ToLower(match[1])
	}

	value = strings.TrimSpace(value)
	match = arxivBareIDPattern.FindStringSubmatch(value)
	if len(match) == 2 {
		return strings.ToLower(match[1])
	}

	// Some extractors emit a textual wrapper such as "arXiv 2401.01234".
	// Strip only leading/trailing letters and whitespace, then validate the
	// remaining value as an ID. Checking the untrimmed value above preserves
	// old-style IDs such as hep-th/9901001, whose leading letters are data.
	value = strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsSpace(r)
	})
	match = arxivBareIDPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(match[1])
}

func catalogMetadataUpdate(
	candidate types.PaperCatalogCandidate,
) types.PaperMetadataUpdate {
	update := types.PaperMetadataUpdate{}
	if title := compactPaperText(candidate.Title); title != "" {
		update.Title = &title
	}
	if abstract := compactPaperText(candidate.Abstract); abstract != "" {
		update.Abstract = &abstract
	}
	authors := make([]types.PaperAuthor, 0, len(candidate.Authors))
	for _, author := range candidate.Authors {
		if name := compactPaperText(author.Name); name != "" {
			author.Name = name
			author.Affiliation = compactPaperText(author.Affiliation)
			authors = append(authors, author)
		}
	}
	if len(authors) > 0 {
		update.Authors = &authors
	}
	tags := make([]string, 0, len(candidate.Tags))
	for _, tag := range candidate.Tags {
		if tag = compactPaperText(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	if len(tags) > maxAutomaticPaperTags {
		tags = tags[:maxAutomaticPaperTags]
	}
	if len(tags) > 0 {
		update.Tags = &tags
	}
	setCatalogMetadataText(&update.SourceName, candidate.SourceName)
	setCatalogMetadataURL(&update.SourceURL, candidate.SourceURL)
	setCatalogMetadataText(&update.Venue, candidate.Venue)
	setCatalogMetadataText(&update.Volume, candidate.Volume)
	setCatalogMetadataText(&update.Issue, candidate.Issue)
	setCatalogMetadataText(&update.Pages, candidate.Pages)
	setCatalogMetadataText(&update.Language, candidate.Language)
	if candidate.PublishedAt != nil {
		update.PublishedAt = candidate.PublishedAt
	}
	if update.HasDataChanges() {
		update.MetadataSource = paperMetadataSourcePointer(types.PaperMetadataSourceCatalog)
	}
	return update
}

func missingCatalogMetadataUpdate(
	current *types.PaperMetadata,
	candidate types.PaperCatalogCandidate,
) types.PaperMetadataUpdate {
	update := catalogMetadataUpdate(candidate)
	if current == nil {
		return update
	}
	if strings.TrimSpace(current.Title) != "" {
		update.Title = nil
	}
	if strings.TrimSpace(current.Abstract) != "" {
		update.Abstract = nil
	}
	if len(current.Authors) > 0 {
		update.Authors = nil
	}
	if len(current.Tags) > 0 {
		update.Tags = nil
	}
	if strings.TrimSpace(current.SourceName) != "" {
		update.SourceName = nil
	}
	if strings.TrimSpace(current.SourceURL) != "" {
		update.SourceURL = nil
	}
	if strings.TrimSpace(current.Venue) != "" {
		update.Venue = nil
	}
	if current.PublishedAt != nil {
		update.PublishedAt = nil
	}
	if strings.TrimSpace(current.Volume) != "" {
		update.Volume = nil
	}
	if strings.TrimSpace(current.Issue) != "" {
		update.Issue = nil
	}
	if strings.TrimSpace(current.Pages) != "" {
		update.Pages = nil
	}
	if strings.TrimSpace(current.Language) != "" {
		update.Language = nil
	}
	if !update.HasDataChanges() {
		update.MetadataSource = nil
	}
	return update
}

func setCatalogMetadataText(destination **string, value string) {
	if value = compactPaperText(value); value != "" {
		*destination = &value
	}
}

func setCatalogMetadataURL(destination **string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*destination = &value
	}
}

func paperMetadataSourcePointer(
	source types.PaperMetadataSource,
) *types.PaperMetadataSource {
	return &source
}

func normalizedPaperTitle(value string) string {
	return strings.Join(strings.FieldsFunc(
		strings.ToLower(value),
		func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) },
	), " ")
}

func paperTitleSearchSeed(normalizedTitle string) string {
	var seed string
	for _, token := range strings.Fields(normalizedTitle) {
		if utf8.RuneCountInString(token) > utf8.RuneCountInString(seed) {
			seed = token
		}
	}
	return seed
}

func paperAuthorNames(authors []types.PaperAuthor) []string {
	names := make([]string, 0, len(authors))
	for _, author := range authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func compactPaperText(value string) string {
	// PostgreSQL text/jsonb rejects U+0000 even though it is valid in Go and
	// protobuf strings. PDF text extraction can preserve embedded NUL bytes,
	// so remove them at the untrusted parser/catalog normalization boundary.
	value = strings.ReplaceAll(value, "\x00", "")
	return strings.Join(strings.Fields(value), " ")
}

func paperInfoContext(
	ctx context.Context,
	requestID string,
	documentID uuid.UUID,
) context.Context {
	if strings.TrimSpace(requestID) != "" {
		ctx = logger.WithRequestID(ctx, strings.TrimSpace(requestID))
	}
	return logger.WithAttrs(
		ctx,
		"document_id", documentID,
		"component", "paper_info",
	)
}

var _ interfaces.PaperInfoService = (*paperInfoService)(nil)
