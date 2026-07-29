package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	// ErrStalePaperInfo means the source document was deleted, changed identity,
	// or its supplied Markdown revision changed while paper-info work was running.
	ErrStalePaperInfo = errors.New("paper info source revision is stale")
	// ErrPaperCatalogNoMatch means providers were reachable but no candidate
	// was sufficiently similar to safely overwrite canonical paper data.
	ErrPaperCatalogNoMatch = errors.New("no satisfactory paper catalog match")
	// ErrPaperInfoExtractionUnavailable means there is no current Markdown
	// revision to submit to the Python extractor.
	ErrPaperInfoExtractionUnavailable = errors.New("paper metadata extraction is unavailable")
	// ErrPaperInfoCatalogUnavailable means durable metadata/reference evidence
	// cannot form a safe external catalog query.
	ErrPaperInfoCatalogUnavailable = errors.New("paper catalog resolution is unavailable")
	// ErrPaperInfoDeleting prevents a manual retry from racing deletion.
	ErrPaperInfoDeleting = errors.New("document is being deleted")

	// paperInfoManualAttempt distinguishes operator-initiated retries from an
	// initial revision-bound task and from Asynq's retries of that task.
	paperInfoManualAttempt atomic.Int64
)

var (
	doiPattern       = regexp.MustCompile(`(?i)\b10\.[0-9]{4,9}/[-._;()/:a-z0-9]+`)
	urlPattern       = regexp.MustCompile(`(?i)https?://[^\s<>()]+`)
	yearTitlePattern = regexp.MustCompile(
		`(?i)\b(?:19|20)[0-9]{2}[a-z]?\s*\.\s+(.+?)(?:\.\s+|$)`,
	)
	arxivIDPattern = regexp.MustCompile(
		`(?i)(?:arxiv\s*:\s*|arxiv\.org/(?:abs|pdf)/|10\.48550/arxiv\.)([a-z-]+(?:\.[a-z-]+)?/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(?:v[0-9]+)?`,
	)
)

// paperInfoService coordinates durable document state, Python extraction,
// external catalogs, and the canonical PaperService boundary.
type paperInfoService struct {
	cfg              config.Config
	documents        interfaces.DocumentRepository
	storage          interfaces.FileStorage
	extractor        interfaces.PaperMetadataExtractor
	catalogs         interfaces.PaperCatalogRegistry
	papers           interfaces.PaperService
	tasks            interfaces.TaskEnqueuer
	inspector        interfaces.TaskInspector
	referenceLimiter interfaces.PaperReferenceCatalogLimiter
	maxConcurrency   int
}

// NewPaperInfoService creates the application-level paper information
// orchestrator. Queue decoding and retry policy remain in the router layer.
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
		maxConcurrency:   concurrency,
	}
}

// ExtractMetadata loads exactly the Markdown revision named by command, asks
// Python for bibliographic candidates, and replaces the current extraction
// snapshot. Revision checks are repeated immediately before each write.
func (s *paperInfoService) ExtractMetadata(
	ctx context.Context,
	command types.PaperMetadataExtractCommand,
) (bool, error) {
	if err := command.Validate(); err != nil {
		return false, err
	}
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	logger.Info(ctx, "paper metadata extraction started")

	markdown, blocks, err := s.loadExtractionSource(ctx, command)
	if err != nil {
		logger.Error(ctx, "paper metadata extraction source unavailable", "error", err)
		return false, err
	}
	result, err := s.extractor.Extract(ctx, &types.PaperExtractionRequest{
		DocumentID:     command.DocumentID,
		MarkdownSHA256: command.MarkdownSHA256,
		Markdown:       markdown,
		Blocks:         blocks,
	})
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, errors.New("paper metadata extractor returned nil result")
	}
	if err := result.Validate(); err != nil {
		return false, err
	}

	update := extractionMetadataUpdate(result)
	references := extractionCitationInputs(result.References)
	if err := s.ensureCurrentRevision(ctx, command.DocumentID, command.MarkdownSHA256); err != nil {
		return false, err
	}
	updatedMetadata, err := s.papers.UpdateMetadata(
		ctx,
		command.DocumentID.String(),
		update,
	)
	if err != nil {
		logger.Error(ctx, "persist extracted paper metadata failed", "error", err)
		return false, fmt.Errorf("persist extracted paper metadata: %w", err)
	}
	if err := s.ensureCurrentRevision(ctx, command.DocumentID, command.MarkdownSHA256); err != nil {
		return false, err
	}
	if _, err := s.papers.ReplaceCitations(
		ctx,
		command.DocumentID.String(),
		references,
	); err != nil {
		logger.Error(ctx, "persist extracted paper references failed", "error", err)
		return false, fmt.Errorf("persist extracted paper references: %w", err)
	}
	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return false, err
	}
	catalogResolvable := hasUsablePaperLookupIdentity(document.DOI, updatedMetadata.Title)
	logger.Info(ctx, "paper metadata extraction persisted",
		"author_count", len(result.Authors),
		"reference_count", len(references),
		"metadata_applied", updatedMetadata.MetadataSource == types.PaperMetadataSourceExtraction,
		"catalog_resolvable", catalogResolvable,
		"extractor", result.ExtractorName)
	return catalogResolvable, nil
}

func hasUsablePaperLookupIdentity(doi, title string) bool {
	return strings.TrimSpace(doi) != "" || strings.TrimSpace(title) != ""
}

// ResolveMetadata queries all configured providers for the source paper and
// each unresolved citation. Provider calls for one query are concurrent but
// bounded; provider order remains the deterministic tie breaker.
func (s *paperInfoService) ResolveMetadata(
	ctx context.Context,
	command types.PaperCatalogResolveCommand,
) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if command.Scope == "" {
		command.Scope = types.PaperCatalogScopeSource
	}
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	ctx = logger.WithAttrs(ctx, "catalog_scope", command.Scope)
	logger.Info(ctx, "paper catalog resolution started")
	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return err
	}
	if err := s.ensureCatalogSourceCurrent(
		ctx,
		document,
		command.MarkdownSHA256,
	); err != nil {
		return err
	}
	if command.Scope == types.PaperCatalogScopeReference {
		return s.resolveReferenceMetadata(ctx, document, command)
	}
	return s.resolveSourceMetadata(ctx, document, command)
}

func (s *paperInfoService) resolveSourceMetadata(ctx context.Context, document types.Document, command types.PaperCatalogResolveCommand) error {
	// Unknown rows are bibliographic placeholders only. They have no source
	// document of their own, so catalog work must always be rooted at the
	// uploaded document that cited them (reference scope).
	if document.Status == types.DocumentStatusUnknown {
		return ErrPaperInfoCatalogUnavailable
	}
	metadata, err := s.papers.GetMetadata(ctx, command.DocumentID.String())
	if err != nil {
		return err
	}
	query := metadataLookupQuery(document, metadata)
	if err := query.Validate(); err != nil {
		return ErrPaperCatalogNoMatch
	}
	selected, found, err := s.selectCatalogCandidate(ctx, query)
	if err != nil {
		return err
	}
	if !found {
		return ErrPaperCatalogNoMatch
	}
	if err := s.ensureCatalogSourceCurrent(ctx, document, command.MarkdownSHA256); err != nil {
		return err
	}
	applied, err := s.applySourceCatalogCandidate(ctx, document, selected)
	if err != nil {
		return err
	}
	if !applied {
		return ErrPaperCatalogNoMatch
	}
	if err := s.enqueueReferenceCatalogTasks(ctx, document, command); err != nil {
		return err
	}
	logger.Info(ctx, "paper catalog source match applied", "provider", selected.Provider, "provider_id", selected.ProviderID, "match_score", selected.MatchScore)
	return nil
}

func (s *paperInfoService) resolveReferenceMetadata(ctx context.Context, source types.Document, command types.PaperCatalogResolveCommand) error {
	if s.referenceLimiter != nil {
		release, err := s.referenceLimiter.AcquireReference(ctx, source.ID.String())
		if err != nil {
			return err
		}
		defer release()
	}
	citations, err := s.papers.ListCitations(ctx, source.ID.String())
	if err != nil {
		return err
	}
	var citation *types.PaperCitation
	for index := range citations {
		if citations[index].CitedDocumentID == command.CitedDocumentID {
			citation = &citations[index]
			break
		}
	}
	if citation == nil {
		return ErrStalePaperInfo
	}
	cited, err := s.documents.Get(ctx, command.CitedDocumentID)
	if err != nil {
		return err
	}
	if cited.Status != types.DocumentStatusUnknown {
		return nil
	}
	query := referenceLookupQuery(citation.RawReference)
	if err := query.Validate(); err != nil {
		return ErrPaperCatalogNoMatch
	}
	selected, found, err := s.selectCatalogCandidate(ctx, query)
	if err != nil {
		return err
	}
	if !found {
		return ErrPaperCatalogNoMatch
	}
	if err := s.ensureCatalogSourceCurrent(ctx, source, command.MarkdownSHA256); err != nil {
		return err
	}
	resolvedID, err := s.findStoredCatalogDocument(ctx, selected, source.ID)
	if err != nil {
		return err
	}
	if resolvedID != uuid.Nil {
		if _, err := s.papers.ResolveCitation(ctx, source.ID.String(), resolvedID.String(), citation.RawReference); err != nil {
			return fmt.Errorf("resolve paper citation: %w", err)
		}
	} else if _, err := s.papers.UpdateMetadata(ctx, cited.ID.String(), catalogMetadataUpdate(selected)); err != nil {
		return fmt.Errorf("persist unresolved citation metadata: %w", err)
	}
	logger.Info(ctx, "paper catalog reference match applied", "cited_document_id", cited.ID, "provider", selected.Provider, "provider_id", selected.ProviderID, "match_score", selected.MatchScore)
	return nil
}

func (s *paperInfoService) applySourceCatalogCandidate(
	ctx context.Context,
	document types.Document,
	selected types.PaperCatalogCandidate,
) (bool, error) {
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
	if strings.TrimSpace(document.DOI) == "" && strings.TrimSpace(selected.DOI) != "" {
		applied, doiErr := s.documents.SetDOIIfEmpty(ctx, document.ID, normalizePaperDOI(selected.DOI))
		if doiErr != nil {
			if errors.Is(doiErr, types.ErrDuplicateDOI) {
				logger.Warn(ctx, "catalog DOI belongs to another document; metadata retained", "document_id", document.ID, "doi", normalizePaperDOI(selected.DOI))
			} else {
				return false, fmt.Errorf("backfill catalog DOI: %w", doiErr)
			}
		} else if applied {
			logger.Info(ctx, "catalog DOI applied to document", "document_id", document.ID, "doi", normalizePaperDOI(selected.DOI))
		}
	}
	return true, nil
}

// enqueueReferenceCatalogTasks creates bounded, independent low-priority
// work after the source-paper lookup commits. A large bibliography therefore
// cannot hold the source task open or monopolise provider calls. Each task
// reloads the citation by placeholder ID; raw reference text never enters
// Redis. Equivalent references are deduplicated before enqueueing.
func (s *paperInfoService) enqueueReferenceCatalogTasks(
	ctx context.Context,
	document types.Document,
	command types.PaperCatalogResolveCommand,
) error {
	if s.tasks == nil {
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
	seen := make(map[string]struct{}, len(citations))
	queued := 0
	for _, citation := range citations {
		if queued >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		cited, err := s.documents.Get(ctx, citation.CitedDocumentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if cited.Status != types.DocumentStatusUnknown {
			continue
		}
		key := referenceCatalogDedupKey(citation.RawReference)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		task, options, err := NewPaperCatalogResolveTask(types.PaperCatalogResolvePayload{
			RequestID: command.RequestID, DocumentID: document.ID.String(),
			MarkdownSHA256: command.MarkdownSHA256, Attempt: command.Attempt,
			Scope: types.PaperCatalogScopeReference, CitedDocumentID: cited.ID.String(),
		}, s.cfg.PaperCatalogTaskTimeout, s.cfg.PaperCatalogTaskMaxRetry)
		if err != nil {
			return fmt.Errorf("build reference catalog task: %w", err)
		}
		if err := enqueueManagedTask(ctx, s.tasks, task, options, document.ID.String()); err != nil {
			return err
		}
		queued++
	}
	logger.Info(ctx, "reference catalog tasks queued", "document_id", document.ID, "queued_count", queued, "citation_count", len(citations), "limit", limit)
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
) ([]byte, []types.PaperSourceBlock, error) {
	if err := s.ensureCurrentRevision(ctx, command.DocumentID, command.MarkdownSHA256); err != nil {
		return nil, nil, err
	}
	manifest, err := s.documents.GetMarkdown(ctx, command.DocumentID)
	if err != nil {
		return nil, nil, err
	}
	reader, err := s.storage.Open(ctx, manifest.StorageKey)
	if err != nil {
		logger.Error(ctx, "open Markdown for paper extraction failed",
			"storage_key", manifest.StorageKey,
			"error", err)
		return nil, nil, err
	}
	defer reader.Close()
	markdown, err := io.ReadAll(io.LimitReader(
		reader,
		int64(types.MaxPaperExtractionMarkdownBytes)+1,
	))
	if err != nil {
		return nil, nil, fmt.Errorf("read Markdown for paper extraction: %w", err)
	}
	if len(markdown) > types.MaxPaperExtractionMarkdownBytes {
		return nil, nil, fmt.Errorf(
			"%w: Markdown exceeds %d bytes",
			types.ErrInvalidPaperExtraction,
			types.MaxPaperExtractionMarkdownBytes,
		)
	}
	persistedBlocks, err := s.documents.ListBlocks(ctx, command.DocumentID, 0)
	if err != nil {
		return nil, nil, err
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
		DocumentID:     command.DocumentID,
		MarkdownSHA256: command.MarkdownSHA256,
		Markdown:       markdown,
		Blocks:         blocks,
	}
	if err := request.Validate(); err != nil {
		return nil, nil, err
	}
	return markdown, blocks, nil
}

func (s *paperInfoService) ensureCurrentRevision(
	ctx context.Context,
	documentID uuid.UUID,
	expectedSHA256 string,
) error {
	document, err := s.documents.Get(ctx, documentID)
	if err != nil {
		return err
	}
	if document.Status != types.DocumentStatusReady {
		return fmt.Errorf(
			"%w: document status is %s",
			ErrStalePaperInfo,
			document.Status,
		)
	}
	if strings.TrimSpace(expectedSHA256) == "" {
		return fmt.Errorf(
			"%w: Markdown SHA-256 is required for a source-backed document",
			ErrStalePaperInfo,
		)
	}
	markdown, err := s.documents.GetMarkdown(ctx, documentID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(
		strings.TrimSpace(markdown.SHA256),
		strings.TrimSpace(expectedSHA256),
	) {
		return fmt.Errorf(
			"%w: expected Markdown SHA-256 %s, got %s",
			ErrStalePaperInfo,
			expectedSHA256,
			markdown.SHA256,
		)
	}
	return nil
}

func (s *paperInfoService) ensureCatalogSourceCurrent(
	ctx context.Context,
	initialDocument types.Document,
	expectedSHA256 string,
) error {
	current, err := s.documents.Get(ctx, initialDocument.ID)
	if err != nil {
		return err
	}
	if current.Status == types.DocumentStatusDeleting {
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
	if initialDocument.Status == types.DocumentStatusUnknown &&
		current.Status != types.DocumentStatusUnknown {
		return fmt.Errorf(
			"%w: source-free paper status changed from unknown to %s",
			ErrStalePaperInfo,
			current.Status,
		)
	}
	if initialDocument.Status != types.DocumentStatusUnknown &&
		current.Status == types.DocumentStatusUnknown {
		return fmt.Errorf(
			"%w: source document changed to a source-free placeholder",
			ErrStalePaperInfo,
		)
	}
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	if expectedSHA256 == "" {
		return nil
	}
	markdown, err := s.documents.GetMarkdown(ctx, initialDocument.ID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(markdown.SHA256), expectedSHA256) {
		return fmt.Errorf(
			"%w: expected Markdown SHA-256 %s, got %s",
			ErrStalePaperInfo,
			expectedSHA256,
			markdown.SHA256,
		)
	}
	return nil
}

func extractionMetadataUpdate(
	result *types.PaperExtractionResult,
) types.PaperMetadataUpdate {
	title := ""
	if result.Title != nil {
		title = compactPaperText(result.Title.Value)
	}
	abstract := ""
	if result.Abstract != nil {
		abstract = compactPaperText(result.Abstract.Value)
	}
	authors := make([]types.PaperAuthor, 0, len(result.Authors))
	for _, candidate := range result.Authors {
		name := compactPaperText(candidate.Value)
		if name != "" {
			authors = append(authors, types.PaperAuthor{Name: name})
		}
	}
	return types.PaperMetadataUpdate{
		Title:          &title,
		Abstract:       &abstract,
		Authors:        &authors,
		MetadataSource: paperMetadataSourcePointer(types.PaperMetadataSourceExtraction),
	}
}

func extractionCitationInputs(references []string) []types.PaperCitationInput {
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
		inputs = append(inputs, types.PaperCitationInput{RawReference: rawReference})
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
) (types.PaperCatalogCandidate, bool, error) {
	if err := query.Validate(); err != nil {
		return types.PaperCatalogCandidate{}, false, err
	}
	providers := s.catalogs.Providers()
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
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(match[1])
}

func catalogMetadataUpdate(
	candidate types.PaperCatalogCandidate,
) types.PaperMetadataUpdate {
	title := compactPaperText(candidate.Title)
	abstract := compactPaperText(candidate.Abstract)
	authors := append([]types.PaperAuthor(nil), candidate.Authors...)
	tags := append([]string(nil), candidate.Tags...)
	if len(tags) > maxAutomaticPaperTags {
		tags = tags[:maxAutomaticPaperTags]
	}
	sourceName := compactPaperText(candidate.SourceName)
	sourceURL := strings.TrimSpace(candidate.SourceURL)
	venue := compactPaperText(candidate.Venue)
	volume := compactPaperText(candidate.Volume)
	issue := compactPaperText(candidate.Issue)
	pages := compactPaperText(candidate.Pages)
	language := compactPaperText(candidate.Language)
	return types.PaperMetadataUpdate{
		Title:       &title,
		Abstract:    &abstract,
		Authors:     &authors,
		Tags:        &tags,
		SourceName:  &sourceName,
		SourceURL:   &sourceURL,
		Venue:       &venue,
		PublishedAt: candidate.PublishedAt,
		Volume:      &volume,
		Issue:       &issue,
		Pages:       &pages,
		Language:    &language,
		MetadataSource: paperMetadataSourcePointer(
			types.PaperMetadataSourceCatalog,
		),
	}
}

func paperMetadataSourcePointer(
	source types.PaperMetadataSource,
) *types.PaperMetadataSource {
	return &source
}

func (s *paperInfoService) findStoredCatalogDocument(
	ctx context.Context,
	candidate types.PaperCatalogCandidate,
	citingDocumentID uuid.UUID,
) (uuid.UUID, error) {
	doi := normalizePaperDOI(candidate.DOI)
	if doi != "" {
		result, err := s.documents.Search(ctx, types.DocumentSearch{
			DOI:      doi,
			Page:     1,
			PageSize: 2,
		})
		if err != nil {
			return uuid.Nil, err
		}
		for _, document := range result.Items {
			if document.ID != citingDocumentID {
				return document.ID, nil
			}
		}
	}

	titleKey := normalizedPaperTitle(candidate.Title)
	if titleKey == "" {
		return uuid.Nil, nil
	}
	result, err := s.papers.SearchMetadata(ctx, types.PaperSearch{
		Title:    paperTitleSearchSeed(titleKey),
		Page:     1,
		PageSize: localPaperTitleSearchSize,
	})
	if err != nil {
		return uuid.Nil, err
	}
	for _, metadata := range result.Items {
		if metadata.DocumentID == citingDocumentID ||
			normalizedPaperTitle(metadata.Title) != titleKey {
			continue
		}
		document, err := s.documents.Get(ctx, metadata.DocumentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return uuid.Nil, err
		}
		if document.Status == types.DocumentStatusUnknown ||
			document.Status == types.DocumentStatusDeleting {
			continue
		}
		if len(candidate.Authors) > 0 &&
			len(metadata.Authors) > 0 &&
			paperAuthorSimilarity(
				paperAuthorNames(candidate.Authors),
				metadata.Authors,
			) == 0 {
			continue
		}
		if candidate.PublishedAt != nil &&
			metadata.PublishedAt != nil &&
			int(math.Abs(float64(
				candidate.PublishedAt.Year()-metadata.PublishedAt.Year(),
			))) > 1 {
			continue
		}
		return document.ID, nil
	}
	return uuid.Nil, nil
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
