package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	minPaperCatalogMatchScore             = 0.72
	paperTitleMatchWeight                 = 0.70
	paperAuthorMatchWeight                = 0.15
	paperYearMatchWeight                  = 0.05
	paperVenueMatchWeight                 = 0.05
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
	// errReferenceProviderFallback asks Asynq for a short ordinary retry so the
	// scheduler can claim another provider. It deliberately carries neither the
	// failed provider's terminal classification nor its long Retry-After value.
	errReferenceProviderFallback = errors.New("reference provider fallback required")
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
	cfg                config.Config
	documents          interfaces.DocumentRepository
	storage            interfaces.FileStorage
	extractor          interfaces.PaperMetadataExtractor
	catalogs           interfaces.PaperCatalogRegistry
	papers             interfaces.PaperService
	tasks              interfaces.TaskEnqueuer
	inspector          interfaces.TaskInspector
	referenceLimiter   interfaces.PaperReferenceCatalogLimiter
	referenceScheduler interfaces.PaperReferenceScheduler
	spans              interfaces.SpanRecorder
	maxConcurrency     int
}

// NewPaperInfoService creates the application-level paper information
// orchestrator. The reference scheduler is an explicit infrastructure
// dependency; queue decoding and retry policy remain in the router layer.
func NewPaperInfoService(
	cfg config.Config,
	documents interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor,
	catalogs interfaces.PaperCatalogRegistry,
	papers interfaces.PaperService,
	tasks interfaces.TaskEnqueuer,
	inspector interfaces.TaskInspector,
	referenceLimiter interfaces.PaperReferenceCatalogLimiter,
	referenceScheduler interfaces.PaperReferenceScheduler,
) interfaces.PaperInfoService {
	svc := newPaperInfoService(cfg, documents, storage, extractor, catalogs, papers, tasks, inspector, referenceLimiter, nil).(*paperInfoService)
	svc.referenceScheduler = referenceScheduler
	return svc
}

// NewPaperInfoServiceWithSpanRecorder is the production DI constructor with
// persistent processing-span support.
func NewPaperInfoServiceWithSpanRecorder(
	cfg config.Config,
	documents interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor,
	catalogs interfaces.PaperCatalogRegistry,
	papers interfaces.PaperService,
	tasks interfaces.TaskEnqueuer,
	inspector interfaces.TaskInspector,
	referenceLimiter interfaces.PaperReferenceCatalogLimiter,
	referenceScheduler interfaces.PaperReferenceScheduler,
	spans interfaces.SpanRecorder,
) interfaces.PaperInfoService {
	svc := newPaperInfoService(cfg, documents, storage, extractor, catalogs, papers, tasks, inspector, referenceLimiter, spans).(*paperInfoService)
	svc.referenceScheduler = referenceScheduler
	return svc
}

func newPaperInfoService(
	cfg config.Config, documents interfaces.DocumentRepository, storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor, catalogs interfaces.PaperCatalogRegistry,
	papers interfaces.PaperService, tasks interfaces.TaskEnqueuer, inspector interfaces.TaskInspector,
	referenceLimiter interfaces.PaperReferenceCatalogLimiter, spans interfaces.SpanRecorder,
) interfaces.PaperInfoService {
	concurrency := cfg.PaperCatalogConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	return &paperInfoService{
		cfg:              cfg,
		documents:        documents,
		storage:          storage,
		extractor:        extractor,
		catalogs:         catalogs,
		papers:           papers,
		tasks:            tasks,
		inspector:        inspector,
		referenceLimiter: referenceLimiter,
		spans:            spans,
		maxConcurrency:   concurrency,
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
	if err := s.enqueueSourceLookupIfNeeded(ctx, command.DocumentID, command.RequestID); err != nil {
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

// ResolveMetadata performs one source-paper or one scheduler-owned reference
// lookup. The task never carries provider assignment or bibliography batches.
func (s *paperInfoService) ResolveMetadata(
	ctx context.Context,
	command types.PaperCatalogResolveCommand,
) (outputErr error) {
	if err := command.Validate(); err != nil {
		return err
	}
	if command.ReferenceID != uuid.Nil {
		return s.ResolveReference(ctx, command)
	}
	_, err := startDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation)
	if err != nil {
		return err
	}
	defer func() {
		finishDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation, outputErr)
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
	return s.resolveSourceMetadata(ctx, document, command, spanID)
}

func paperAttempt(attempt int) int {
	if attempt < 1 {
		return 1
	}
	return attempt
}

func (s *paperInfoService) resolveSourceMetadata(ctx context.Context, document types.Document, command types.PaperCatalogResolveCommand, spanID uuid.UUID) error {
	metadata, err := s.papers.GetMetadata(ctx, document.ID.String())
	if err != nil {
		return err
	}
	query := metadataLookupQuery(document, metadata)
	if err := query.Validate(); err != nil {
		logger.Info(ctx, "source catalog lookup skipped", "document_id", document.ID, "reason", "title and DOI are unavailable")
		return nil
	}
	if err := ensureDocumentStageWork(ctx, s.documents, document.ID, command.Stage, command.Generation); err != nil {
		return err
	}
	selected, found, err := s.selectCatalogCandidate(
		ctx,
		query,
		document.ID,
		stageAttempt(command.Generation, 0),
		types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogSource, document.ID.String()),
		spanID,
	)
	if err != nil {
		return err
	}
	if !found {
		logger.Info(ctx, "source catalog lookup completed without match", "document_id", document.ID)
		return nil
	}
	if err := ensureDocumentStageWork(ctx, s.documents, document.ID, command.Stage, command.Generation); err != nil {
		return err
	}
	if err := s.ensureCatalogSourceCurrent(ctx, document); err != nil {
		return err
	}
	applied, err := s.applySourceCatalogCandidate(ctx, document, selected)
	if err != nil {
		return err
	}
	if !applied {
		logger.Info(ctx, "source catalog candidate not applied", "document_id", document.ID)
		return nil
	}
	logger.Info(ctx, "paper catalog source match applied", "provider", selected.Provider, "provider_id", selected.ProviderID, "match_score", selected.MatchScore)
	return nil
}

// DispatchReferences turns the citation snapshot persisted by metadata
// extraction into bounded provider batches. It deliberately does not depend on
// source-paper catalog success: a missing source match must not suppress valid
// bibliography resolution, and this dispatcher never makes provider calls.
func (s *paperInfoService) DispatchReferences(
	ctx context.Context,
	command types.PaperReferenceDispatchCommand,
) (outputErr error) {
	if err := command.Validate(); err != nil {
		return err
	}
	_, err := startDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation)
	if err != nil {
		return err
	}
	defer func() {
		finishDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation, outputErr)
	}()
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	logger.Info(ctx, "paper reference dispatch started")

	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return err
	}
	if err := s.ensureCatalogSourceCurrent(ctx, document); err != nil {
		return err
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return err
	}
	if err := s.enqueueReferenceCatalogTasks(ctx, document, command); err != nil {
		return err
	}
	return nil
}

// ResolveReference executes one provider attempt for one citation row owned by
// its citing paper, then reports the result to the global scheduler. Local
// DOI/title matching runs before any provider request and again after a
// candidate is selected, so an already-imported paper never causes an external
// lookup and a stale provider never wins over local data.
func (s *paperInfoService) ResolveReference(
	ctx context.Context,
	command types.PaperCatalogResolveCommand,
) (outputErr error) {
	if command.ReferenceID == uuid.Nil || s.referenceScheduler == nil {
		return ErrPaperInfoCatalogUnavailable
	}
	reference, applied, err := s.papers.StartReferenceResolution(ctx, command.ReferenceID, command.Generation)
	if err != nil {
		return err
	}
	if !applied {
		return fmt.Errorf("%w: reference resolution generation %d is no longer active", asynq.RevokeTask, command.Generation)
	}
	defer func() {
		if outputErr == nil || errors.Is(outputErr, asynq.RevokeTask) {
			return
		}
		status := types.DocumentStageQueued
		code, message := "", ""
		if errors.Is(outputErr, asynq.SkipRetry) || documentStageErrorTerminal(outputErr) || finalAsynqAttempt(ctx) {
			status = types.DocumentStageFailed
			code, message = spanFailure(outputErr)
		}
		if _, _, finishErr := s.papers.FinishReferenceResolution(
			context.WithoutCancel(ctx), command.ReferenceID, command.Generation,
			status, "", code, message, nil, nil,
		); finishErr != nil {
			logger.Warn(ctx, "finish failed reference resolution task failed",
				"reference_id", command.ReferenceID, "error", finishErr)
		}
	}()

	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	ctx = logger.WithAttrs(ctx, "catalog_scope", "reference")
	if reference.CitedDocumentID != nil {
		return s.finishReferenceSuccess(ctx, command, "", nil, nil)
	}
	excludeDocumentID := reference.CitingDocumentID
	if excludeDocumentID == uuid.Nil {
		excludeDocumentID = command.DocumentID
	}
	if documentID, found, matchErr := s.papers.FindLocalReferenceMatch(ctx, reference, excludeDocumentID); matchErr != nil {
		return matchErr
	} else if found {
		logger.Info(ctx, "reference matched local library before provider lookup",
			"reference_id", command.ReferenceID, "document_id", documentID)
		return s.finishReferenceSuccess(ctx, command, "", nil, &documentID)
	}

	claim, err := s.referenceScheduler.Claim(ctx, command.ReferenceID)
	if err != nil {
		return err
	}
	if !claim.Ready() {
		// A completed scheduler job means every provider was exhausted; a
		// non-completed in-flight job must stay queued for a later retry rather
		// than being reported as a successful no-op.
		if claim.Job.Completed {
			return s.finishReferenceSuccess(ctx, command, "", nil, nil)
		}
		return errReferenceProviderFallback
	}
	if s.referenceLimiter != nil {
		release, acquireErr := s.referenceLimiter.AcquireReference(ctx, command.ReferenceID.String())
		if acquireErr != nil {
			return acquireErr
		}
		defer release()
	}

	query := referenceLookupQueryFor(reference)
	if err := query.Validate(); err != nil {
		reportErr := s.reportReferenceOutcome(ctx, command.ReferenceID, claim.Provider, types.PaperReferenceOutcomeTerminalFailure, nil)
		if reportErr != nil {
			return reportErr
		}
		return s.finishReferenceSuccess(ctx, command, claim.Provider.String(), nil, nil)
	}
	logger.Debug(ctx, "reference catalog lookup query selected",
		"reference_id", command.ReferenceID,
		"has_doi", strings.TrimSpace(query.DOI) != "",
		"has_title", strings.TrimSpace(query.Title) != "",
		"has_source_url", strings.TrimSpace(query.SourceURL) != "",
		"author_count", len(query.Authors),
	)

	provider, ok := s.catalogs.Get(claim.Provider.String())
	if !ok {
		return s.reportReferenceFailure(ctx, command, claim.Provider, fmt.Errorf("catalog provider %q is unavailable", claim.Provider))
	}
	scopeID := types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogReference, command.ReferenceID.String())
	selected, found, err := s.selectCatalogCandidateFromProviders(
		ctx, query, command.DocumentID, 1, scopeID, uuid.Nil, []interfaces.PaperCatalogProvider{provider},
	)
	if err != nil {
		return s.reportReferenceFailure(ctx, command, claim.Provider, err)
	}
	if !found {
		if reportErr := s.reportReferenceOutcome(ctx, command.ReferenceID, claim.Provider, types.PaperReferenceOutcomeNoMatch, nil); reportErr != nil {
			return reportErr
		}
		return s.finishReferenceSuccess(ctx, command, claim.Provider.String(), nil, nil)
	}
	if selected.DOI == "" && query.DOI != "" {
		selected.DOI = query.DOI
	}

	patch := referenceMetadataPatchFromCatalog(selected)
	candidateReference := reference
	if selected.DOI != "" {
		candidateReference.DOI = selected.DOI
	}
	if selected.Title != "" {
		candidateReference.Title = selected.Title
	}
	if len(selected.Authors) > 0 {
		candidateReference.Authors = selected.Authors
	}
	if selected.PublishedAt != nil {
		candidateReference.PublishedAt = selected.PublishedAt
	}
	var resolvedDocumentID *uuid.UUID
	if documentID, hit, matchErr := s.papers.FindLocalReferenceMatch(ctx, candidateReference, excludeDocumentID); matchErr != nil {
		return s.reportReferenceFailure(ctx, command, claim.Provider, matchErr)
	} else if hit {
		resolvedDocumentID = &documentID
	}
	if reportErr := s.reportReferenceOutcome(ctx, command.ReferenceID, claim.Provider, types.PaperReferenceOutcomeMatched, nil); reportErr != nil {
		return reportErr
	}
	return s.finishReferenceSuccess(ctx, command, claim.Provider.String(), patch, resolvedDocumentID)
}

func (s *paperInfoService) reportReferenceFailure(
	ctx context.Context,
	command types.PaperCatalogResolveCommand,
	provider types.PaperCatalogProvider,
	cause error,
) error {
	reportErr := s.reportReferenceOutcome(ctx, command.ReferenceID, provider, types.PaperReferenceOutcomeRetryableFailure, cause)
	if reportErr != nil {
		return reportErr
	}
	return s.finishReferenceSuccess(ctx, command, provider.String(), nil, nil)
}

func (s *paperInfoService) finishReferenceSuccess(
	ctx context.Context,
	command types.PaperCatalogResolveCommand,
	provider string,
	patch *types.PaperReferenceMetadataPatch,
	resolvedDocumentID *uuid.UUID,
) error {
	_, applied, err := s.papers.FinishReferenceResolution(
		context.WithoutCancel(ctx), command.ReferenceID, command.Generation,
		types.DocumentStageSucceeded, provider, "", "", patch, resolvedDocumentID,
	)
	if err != nil {
		return err
	}
	if !applied {
		logger.Info(ctx, "reference resolution finish ignored after generation change",
			"reference_id", command.ReferenceID, "generation", command.Generation)
	}
	return nil
}

func referenceLookupQueryFor(reference types.PaperReference) types.PaperLookupQuery {
	query := types.PaperLookupQuery{
		DOI:       normalizePaperDOI(reference.DOI),
		Title:     strings.TrimSpace(reference.Title),
		SourceURL: strings.TrimSpace(reference.SourceURL),
		Venue:     strings.TrimSpace(reference.Venue),
		Limit:     types.DefaultPaperCatalogLimit,
	}
	for _, author := range reference.Authors {
		if name := strings.TrimSpace(author.Name); name != "" {
			query.Authors = append(query.Authors, name)
		}
	}
	if reference.PublishedAt != nil {
		query.PublishedYear = reference.PublishedAt.Year()
	}
	if query.Validate() != nil {
		return referenceLookupQuery(reference.RawReference)
	}
	return query
}

func referenceMetadataPatchFromCatalog(candidate types.PaperCatalogCandidate) *types.PaperReferenceMetadataPatch {
	patch := &types.PaperReferenceMetadataPatch{}
	if doi := normalizePaperDOI(candidate.DOI); doi != "" {
		patch.DOI = &doi
	}
	if title := compactPaperText(candidate.Title); title != "" {
		patch.Title = &title
	}
	authors := make([]types.PaperAuthor, 0, len(candidate.Authors))
	for _, author := range candidate.Authors {
		author.Name = compactPaperText(author.Name)
		author.Affiliation = compactPaperText(author.Affiliation)
		if author.Name != "" {
			authors = append(authors, author)
		}
	}
	if len(authors) > 0 {
		patch.Authors = &authors
	}
	setCatalogMetadataText(&patch.Venue, candidate.Venue)
	setCatalogMetadataText(&patch.Volume, candidate.Volume)
	setCatalogMetadataText(&patch.Issue, candidate.Issue)
	setCatalogMetadataText(&patch.Pages, candidate.Pages)
	setCatalogMetadataURL(&patch.SourceURL, candidate.SourceURL)
	if candidate.PublishedAt != nil {
		patch.PublishedAt = candidate.PublishedAt
	}
	if patch.HasChanges() {
		source := types.PaperMetadataSourceCatalog
		patch.MetadataSource = &source
	}
	return patch
}

func (s *paperInfoService) reportReferenceOutcome(ctx context.Context, target uuid.UUID, provider types.PaperCatalogProvider, outcome types.PaperReferenceOutcome, cause error) error {
	if cause != nil && outcome == types.PaperReferenceOutcomeRetryableFailure {
		var retryable interface{ TaskRetryable() bool }
		if errors.As(cause, &retryable) && retryable.TaskRetryable() {
			delay := 3 * s.cfg.PaperCatalogSpacingFor(provider.String())
			var retryAfter interface{ TaskRetryAfter() time.Duration }
			if errors.As(cause, &retryAfter) && retryAfter.TaskRetryAfter() > 0 {
				delay = retryAfter.TaskRetryAfter()
			}
			if delay < time.Second {
				delay = time.Second
			}
			if err := s.referenceScheduler.Cooldown(context.WithoutCancel(ctx), provider, delay); err != nil {
				logger.Warn(ctx, "reference scheduler provider cooldown update failed", "provider", provider, "error", err)
			}
		}
	}
	report, err := s.referenceScheduler.Report(context.WithoutCancel(ctx), target, provider, outcome)
	if err != nil {
		return err
	}
	if cause != nil {
		logger.Warn(ctx, "reference catalog provider attempt failed", "document_id", target, "provider", provider, "error", cause)
	}
	if report.Requeue {
		return errReferenceProviderFallback
	}
	// Provider failures have already been converted into scheduler-owned
	// fallback work. A Requeue report returns the neutral error above so this
	// same stable task can claim the next provider without inheriting the
	// previous provider's terminal classification or cooldown delay.
	return nil
}

func (s *paperInfoService) applySourceCatalogCandidate(
	ctx context.Context,
	document types.Document,
	selected types.PaperCatalogCandidate,
) (bool, error) {
	doi := normalizePaperDOI(selected.DOI)
	if strings.TrimSpace(document.DOI) == "" && doi != "" {
		page, err := s.documents.Search(ctx, types.DocumentSearch{
			DOI: doi, SourceState: types.DocumentSourceAvailable, Page: 1, PageSize: 2,
		})
		if err != nil {
			return false, err
		}
		for _, canonical := range page.Items {
			if canonical.ID == document.ID {
				continue
			}
			if err := s.documents.MergeIntoCanonical(ctx, document.ID, canonical.ID); err != nil {
				return false, err
			}
			s.deleteMergedSourceObject(ctx, document)
			return true, nil
		}
	}
	updated, err := s.papers.UpdateMetadata(
		ctx,
		document.ID.String(),
		catalogMetadataUpdate(selected),
	)
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
	if strings.TrimSpace(document.DOI) == "" && doi != "" {
		applied, doiErr := s.documents.SetDOIIfEmpty(ctx, document.ID, doi)
		if doiErr != nil {
			if errors.Is(doiErr, types.ErrDuplicateDOI) {
				page, lookupErr := s.documents.Search(ctx, types.DocumentSearch{
					DOI: doi, SourceState: types.DocumentSourceAvailable, Page: 1, PageSize: 2,
				})
				if lookupErr != nil {
					return false, lookupErr
				}
				for _, canonical := range page.Items {
					if canonical.ID == document.ID {
						continue
					}
					if mergeErr := s.documents.MergeIntoCanonical(ctx, document.ID, canonical.ID); mergeErr != nil {
						return false, mergeErr
					}
					s.deleteMergedSourceObject(ctx, document)
					return true, nil
				}
				return false, fmt.Errorf("backfill catalog DOI: %w", doiErr)
			} else {
				return false, fmt.Errorf("backfill catalog DOI: %w", doiErr)
			}
		} else if applied {
			logger.Info(ctx, "catalog DOI applied to document", "document_id", document.ID, "doi", normalizePaperDOI(selected.DOI))
		}
	}
	return true, nil
}

func (s *paperInfoService) deleteMergedSourceObject(ctx context.Context, document types.Document) {
	if s.storage == nil || strings.TrimSpace(document.StorageKey) == "" {
		return
	}
	if err := s.storage.Delete(context.WithoutCancel(ctx), document.StorageKey); err != nil {
		logger.Warn(ctx, "delete merged document source failed", "document_id", document.ID, "storage_key", document.StorageKey, "error", err)
	}
}

// enqueueReferenceCatalogTasks registers each unique unresolved citation row
// with the global scheduler, then queues one small catalog task per accepted
// target. Provider choice is deferred until that task actually executes.
func (s *paperInfoService) enqueueReferenceCatalogTasks(
	ctx context.Context,
	document types.Document,
	command types.PaperReferenceDispatchCommand,
) error {
	if s.tasks == nil || s.referenceScheduler == nil {
		return nil
	}
	citations, err := s.papers.ListCitations(ctx, document.ID.String())
	if err != nil {
		return err
	}
	limit := s.cfg.PaperReferenceCatalogMaxPerDocument
	if limit <= 0 {
		limit = defaultReferenceCatalogMaxPerDocument
	}
	seen := make(map[uuid.UUID]struct{}, len(citations))
	candidates := make([]types.PaperReference, 0, len(citations))
	for _, citation := range citations {
		if len(candidates) >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if citation.CitedDocumentID != nil {
			continue
		}
		if _, exists := seen[citation.ReferenceID]; exists {
			continue
		}
		seen[citation.ReferenceID] = struct{}{}
		candidates = append(candidates, citation.PaperReference)
	}
	if len(candidates) == 0 {
		logger.Info(ctx, "reference dispatch completed without unresolved references", "document_id", document.ID)
		return nil
	}
	queued := 0
	for _, reference := range candidates {
		updated, applied, queueErr := s.papers.QueueReferenceResolution(ctx, reference.ReferenceID)
		if queueErr != nil {
			return queueErr
		}
		if !applied {
			continue
		}
		shouldQueue, registerErr := s.referenceScheduler.Register(ctx, types.PaperReferenceJob{
			TargetDocumentID: reference.ReferenceID,
			Generation:       updated.ResolutionGeneration,
		})
		if registerErr != nil {
			return registerErr
		}
		if !shouldQueue {
			continue
		}
		task, options, buildErr := NewPaperCatalogResolveTask(types.PaperCatalogResolvePayload{
			RequestID:   command.RequestID,
			DocumentID:  document.ID.String(),
			ReferenceID: reference.ReferenceID.String(),
			Generation:  updated.ResolutionGeneration,
		}, s.cfg.PaperCatalogTaskTimeout, s.cfg.PaperCatalogTaskMaxRetry)
		if buildErr != nil {
			return fmt.Errorf("build reference resolution task: %w", buildErr)
		}
		if enqueueErr := enqueueDocumentStageTask(ctx, s.tasks, task, options, document.ID.String()); enqueueErr != nil {
			_, _, _ = s.papers.FinishReferenceResolution(
				context.WithoutCancel(ctx), reference.ReferenceID, updated.ResolutionGeneration,
				types.DocumentStageFailed, "", types.ProcessingErrorTaskFailed,
				"reference task could not be queued", nil, nil,
			)
			return enqueueErr
		}
		queued++
	}
	logger.Info(ctx, "independent references queued",
		"document_id", document.ID, "queued_count", queued, "citation_count", len(citations), "limit", limit)
	return nil
}

func referenceCatalogDedupKey(rawReference string) string {
	query := referenceLookupQuery(rawReference)
	if doi := normalizePaperDOI(query.DOI); doi != "" {
		return "doi:" + doi
	}
	if arxiv := paperArXivID(query.SourceURL); arxiv != "" {
		return "arxiv:" + arxiv
	}
	if url := strings.ToLower(strings.TrimSpace(query.SourceURL)); url != "" {
		return "url:" + url
	}
	if raw := normalizedPaperTitle(rawReference); raw != "" {
		return "raw:" + raw
	}
	return ""
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
	for _, rawReference := range references {
		rawReference = strings.TrimSpace(rawReference)
		key := compactPaperText(rawReference)
		if rawReference == "" || key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		input := types.PaperCitationInput{RawReference: rawReference}
		if candidate, exists := candidateByReference[key]; exists {
			candidate.RawReference = rawReference
			input.Candidate = &candidate
		}
		inputs = append(inputs, input)
	}
	return inputs
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

// reference recognition
func referenceLookupQuery(rawReference string) types.PaperLookupQuery {
	query := types.PaperLookupQuery{Limit: types.DefaultPaperCatalogLimit}
	if match := doiPattern.FindString(rawReference); match != "" {
		query.DOI = normalizePaperDOI(match)
	}
	if match := urlPattern.FindString(rawReference); match != "" {
		query.SourceURL = strings.TrimRight(match, ".,;:")
	}
	if match := arxivIDPattern.FindStringSubmatch(rawReference); len(match) == 2 {
		query.SourceURL = "https://arxiv.org/abs/" + strings.ToLower(match[1])
	}
	if match := yearTitlePattern.FindStringSubmatch(rawReference); len(match) == 2 {
		query.Title = strings.Trim(match[1], " \t\r\n_*")
	} else if query.DOI == "" && query.SourceURL == "" {
		query.Title = strings.Trim(strings.TrimSpace(rawReference), "-*_ ")
	}
	return query
}

type providerSearchResult struct {
	candidates []types.PaperCatalogCandidate
	err        error
}

func (s *paperInfoService) selectCatalogCandidate(
	ctx context.Context,
	query types.PaperLookupQuery,
	documentID uuid.UUID,
	attempt int,
	spanScopeID types.ProcessingSpanScopeID,
	parentSpanID uuid.UUID,
) (types.PaperCatalogCandidate, bool, error) {
	return s.selectCatalogCandidateFromProviders(ctx, query, documentID, attempt, spanScopeID, parentSpanID, s.catalogs.Providers())
}

func (s *paperInfoService) selectCatalogCandidateFromProviders(
	ctx context.Context,
	query types.PaperLookupQuery,
	documentID uuid.UUID,
	attempt int,
	spanScopeID types.ProcessingSpanScopeID,
	parentSpanID uuid.UUID,
	providers []interfaces.PaperCatalogProvider,
) (types.PaperCatalogCandidate, bool, error) {
	if err := query.Validate(); err != nil {
		return types.PaperCatalogCandidate{}, false, err
	}
	if len(providers) == 0 {
		return types.PaperCatalogCandidate{}, false, errors.New("no paper catalog providers configured")
	}
	results := make([]providerSearchResult, len(providers))
	limit := s.maxConcurrency
	if limit > len(providers) {
		limit = len(providers)
	}
	semaphore := make(chan struct{}, limit)
	var wait sync.WaitGroup
	for index, provider := range providers {
		wait.Add(1)
		go func(index int, provider interfaces.PaperCatalogProvider) {
			defer wait.Done()
			var providerSpan uuid.UUID
			if s.spans != nil {
				if parentSpanID != uuid.Nil {
					providerSpan, _ = s.spans.BeginChildSpan(ctx, parentSpanID, documentID, attempt, types.ProcessingSpanCatalogProvider, types.ProcessingSpanScopeProvider, types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogProvider, spanScopeID.String(), types.NormalizePaperCatalogProvider(provider.Name()).String()), 0)
				} else {
					providerSpan, _ = s.spans.BeginSpan(ctx, documentID, attempt, types.ProcessingSpanCatalogProvider, types.ProcessingSpanScopeProvider, types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogProvider, spanScopeID.String(), types.NormalizePaperCatalogProvider(provider.Name()).String()), 0)
				}
			}
			stopProviderHeartbeat := keepSpanAlive(ctx, s.spans, providerSpan)
			defer stopProviderHeartbeat()
			select {
			// require sema
			case semaphore <- struct{}{}:
				// release sema
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			results[index].candidates, results[index].err = provider.Search(ctx, query)
			if s.spans != nil {
				if results[index].err != nil {
					code, reason := spanFailure(results[index].err)
					_ = s.spans.FinishSpan(context.WithoutCancel(ctx), providerSpan, types.ProcessingSpanFailed, code, reason)
				} else {
					_ = s.spans.FinishSpan(context.WithoutCancel(ctx), providerSpan, types.ProcessingSpanSucceeded, "", "")
				}
			}
		}(index, provider)
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return types.PaperCatalogCandidate{}, false, err
	}

	type rankedCandidate struct {
		candidate     types.PaperCatalogCandidate
		exact         bool
		providerOrder int
		resultOrder   int
	}
	ranked := make([]rankedCandidate, 0)
	successCount := 0
	var providerErrors []error
	for providerIndex, result := range results {
		providerName := providers[providerIndex].Name()
		if result.err != nil {
			providerErrors = append(
				providerErrors,
				fmt.Errorf("%s: %w", providerName, result.err),
			)
			logger.Warn(ctx, "paper catalog provider lookup failed",
				"provider", providerName,
				"error", result.err)
			continue
		}
		successCount++
		for resultIndex, candidate := range result.candidates {
			if err := candidate.Validate(); err != nil {
				logger.Warn(ctx, "paper catalog candidate ignored",
					"provider", providerName,
					"error", err)
				continue
			}
			score, exact := scorePaperCatalogCandidate(query, candidate)
			candidate.MatchScore = score
			ranked = append(ranked, rankedCandidate{
				candidate:     candidate,
				exact:         exact,
				providerOrder: providerIndex,
				resultOrder:   resultIndex,
			})
		}
	}
	if successCount == 0 {
		return types.PaperCatalogCandidate{}, false, errors.Join(providerErrors...)
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].exact != ranked[right].exact {
			return ranked[left].exact
		}
		if ranked[left].candidate.MatchScore != ranked[right].candidate.MatchScore {
			return ranked[left].candidate.MatchScore > ranked[right].candidate.MatchScore
		}
		if ranked[left].providerOrder != ranked[right].providerOrder {
			return ranked[left].providerOrder < ranked[right].providerOrder
		}
		return ranked[left].resultOrder < ranked[right].resultOrder
	})
	if len(ranked) == 0 ||
		(!ranked[0].exact && ranked[0].candidate.MatchScore < minPaperCatalogMatchScore) {
		return types.PaperCatalogCandidate{}, false, nil
	}
	return ranked[0].candidate, true, nil
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

	weighted, weight := 0.0, 0.0
	if strings.TrimSpace(query.Title) != "" {
		weighted += paperTitleMatchWeight * paperTextSimilarity(query.Title, candidate.Title)
		weight += paperTitleMatchWeight
	}
	if len(query.Authors) > 0 {
		weighted += paperAuthorMatchWeight * paperAuthorSimilarity(query.Authors, candidate.Authors)
		weight += paperAuthorMatchWeight
	}
	if query.PublishedYear > 0 {
		yearScore := 0.0
		if candidate.PublishedAt != nil {
			difference := int(math.Abs(float64(
				query.PublishedYear - candidate.PublishedAt.Year(),
			)))
			switch difference {
			case 0:
				yearScore = 1
			case 1:
				yearScore = 0.5
			}
		}
		weighted += paperYearMatchWeight * yearScore
		weight += paperYearMatchWeight
	}
	if strings.TrimSpace(query.Venue) != "" {
		weighted += paperVenueMatchWeight * paperTextSimilarity(query.Venue, candidate.Venue)
		weight += paperVenueMatchWeight
	}
	weighted += paperProviderScoreWeight * candidate.ProviderScore
	weight += paperProviderScoreWeight
	if weight == 0 {
		return 0, false
	}
	score := weighted / weight
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
