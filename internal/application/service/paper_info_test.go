package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/infrastructure/papercatalog"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// newPaperInfoServiceForTest keeps test setup concise while still injecting
// the same infrastructure memory scheduler used by Lite mode. Production
// constructors must receive their scheduler from DI explicitly.
func newPaperInfoServiceForTest(
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
	schedulerCfg := cfg
	if len(schedulerCfg.PaperCatalogProviders) == 0 && catalogs != nil {
		for _, provider := range catalogs.Providers() {
			schedulerCfg.PaperCatalogProviders = append(
				schedulerCfg.PaperCatalogProviders, provider.Name(),
			)
		}
	}
	return NewPaperInfoService(
		cfg, documents, storage, extractor, catalogs, papers, tasks, inspector,
		referenceLimiter, papercatalog.NewMemoryReferenceScheduler(schedulerCfg),
	)
}

// registerReferenceJobForTest mirrors the production dispatcher boundary:
// reference workers may claim only jobs that were registered beforehand.
func registerReferenceJobForTest(
	t *testing.T,
	service interfaces.PaperInfoService,
	targetDocumentID uuid.UUID,
	sourceDocumentID uuid.UUID,
) {
	t.Helper()
	implementation, ok := service.(*paperInfoService)
	if !ok || implementation.referenceScheduler == nil {
		t.Fatal("paper info service has no reference scheduler")
	}
	if _, err := implementation.referenceScheduler.Register(context.Background(), types.PaperReferenceJob{
		TargetDocumentID: targetDocumentID,
	}); err != nil {
		t.Fatalf("register reference job: %v", err)
	}
}

type paperInfoStorageStub struct {
	content map[string][]byte
}

func (*paperInfoStorageStub) CheckConnectivity(context.Context) error { return nil }
func (*paperInfoStorageStub) Save(
	context.Context, interfaces.FileUpload,
) (interfaces.StoredFile, error) {
	return interfaces.StoredFile{}, errors.New("not implemented")
}
func (*paperInfoStorageStub) SaveBytes(
	context.Context, string, []byte,
) (interfaces.StoredFile, error) {
	return interfaces.StoredFile{}, errors.New("not implemented")
}
func (*paperInfoStorageStub) SaveFromURL(
	context.Context, interfaces.SaveFromURLRequest,
) (interfaces.SaveFromURLResult, error) {
	return interfaces.SaveFromURLResult{}, errors.New("not implemented")
}
func (*paperInfoStorageStub) Copy(
	context.Context, interfaces.StoredFile,
) (interfaces.StoredFile, error) {
	return interfaces.StoredFile{}, errors.New("not implemented")
}
func (s *paperInfoStorageStub) Open(
	_ context.Context, key string,
) (io.ReadCloser, error) {
	content, ok := s.content[key]
	if !ok {
		return nil, errors.New("missing test object")
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}
func (*paperInfoStorageStub) Delete(context.Context, string) error { return nil }

type paperInfoExtractorStub struct {
	extract func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error)
}

func (s *paperInfoExtractorStub) Extract(
	_ context.Context,
	request *types.PaperExtractionRequest,
) (*types.PaperExtractionResult, error) {
	return s.extract(request)
}

type paperInfoProviderStub struct {
	name   string
	search func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error)
}

func (p *paperInfoProviderStub) Name() string { return p.name }
func (p *paperInfoProviderStub) Search(
	_ context.Context,
	query types.PaperLookupQuery,
) ([]types.PaperCatalogCandidate, error) {
	return p.search(query)
}

type paperInfoRegistryStub struct {
	providers []interfaces.PaperCatalogProvider
}

func (r *paperInfoRegistryStub) Get(
	name string,
) (interfaces.PaperCatalogProvider, bool) {
	for _, provider := range r.providers {
		if provider.Name() == name {
			return provider, true
		}
	}
	return nil, false
}
func (r *paperInfoRegistryStub) Providers() []interfaces.PaperCatalogProvider {
	return append([]interfaces.PaperCatalogProvider(nil), r.providers...)
}

type paperInfoSpanStart struct {
	documentID uuid.UUID
	parentID   uuid.UUID
	attempt    int
	kind       types.ProcessingSpanKind
	scope      types.ProcessingSpanScope
	scopeID    types.ProcessingSpanScopeID
}

type paperInfoSpanStub struct {
	mu     sync.Mutex
	starts []paperInfoSpanStart
}

func (s *paperInfoSpanStub) BeginSpan(
	_ context.Context,
	documentID uuid.UUID,
	attempt int,
	kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	_ int,
) (uuid.UUID, error) {
	return s.record(uuid.Nil, documentID, attempt, kind, scope, scopeID)
}

func (s *paperInfoSpanStub) record(
	parentID, documentID uuid.UUID,
	attempt int,
	kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
) (uuid.UUID, error) {
	s.mu.Lock()
	s.starts = append(s.starts, paperInfoSpanStart{
		documentID: documentID,
		parentID:   parentID,
		attempt:    attempt,
		kind:       kind,
		scope:      scope,
		scopeID:    scopeID,
	})
	s.mu.Unlock()
	return uuid.New(), nil
}
func (s *paperInfoSpanStub) BeginChildSpan(
	ctx context.Context,
	parentSpanID uuid.UUID,
	documentID uuid.UUID,
	attempt int,
	kind types.ProcessingSpanKind,
	scope types.ProcessingSpanScope,
	scopeID types.ProcessingSpanScopeID,
	retryCount int,
) (uuid.UUID, error) {
	_ = ctx
	_ = retryCount
	return s.record(parentSpanID, documentID, attempt, kind, scope, scopeID)
}
func (*paperInfoSpanStub) Heartbeat(context.Context, uuid.UUID) error { return nil }
func (*paperInfoSpanStub) FinishSpan(context.Context, uuid.UUID, types.ProcessingSpanStatus, string, string) error {
	return nil
}
func (*paperInfoSpanStub) CancelStageProjection(context.Context, uuid.UUID, types.DocumentStage, int64, string, string) error {
	return nil
}
func (*paperInfoSpanStub) AggregateParentDisplay(context.Context, uuid.UUID) (types.ProcessingSpanStatus, error) {
	return "", nil
}

type paperInfoMetadataWrite struct {
	documentID string
	update     types.PaperMetadataUpdate
}

type paperInfoPaperServiceStub struct {
	mu            sync.Mutex
	metadata      map[string]types.PaperMetadata
	citations     map[string][]types.PaperCitation
	updates       []paperInfoMetadataWrite
	replacements  map[string][]types.PaperCitationInput
	merges        map[string][]types.PaperCitationInput
	resolutions   []types.PaperCitation
	updateHook    func(string, types.PaperMetadataUpdate)
	replaceHook   func(string, []types.PaperCitationInput)
	resolveResult *types.PaperCitation
}

func newPaperInfoPaperServiceStub() *paperInfoPaperServiceStub {
	return &paperInfoPaperServiceStub{
		metadata:     make(map[string]types.PaperMetadata),
		citations:    make(map[string][]types.PaperCitation),
		replacements: make(map[string][]types.PaperCitationInput),
		merges:       make(map[string][]types.PaperCitationInput),
	}
}

func (s *paperInfoPaperServiceStub) GetMetadata(
	_ context.Context, documentID string,
) (*types.PaperMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.metadata[documentID]
	return &value, nil
}

func (s *paperInfoPaperServiceStub) UpdateMetadata(
	_ context.Context,
	documentID string,
	update types.PaperMetadataUpdate,
) (*types.PaperMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, paperInfoMetadataWrite{
		documentID: documentID,
		update:     update,
	})
	value := s.metadata[documentID]
	if update.Title != nil {
		value.Title = *update.Title
	}
	if update.Abstract != nil {
		value.Abstract = *update.Abstract
	}
	if update.Authors != nil {
		value.Authors = append([]types.PaperAuthor(nil), (*update.Authors)...)
	}
	if update.MetadataSource != nil {
		value.MetadataSource = *update.MetadataSource
	}
	s.metadata[documentID] = value
	if s.updateHook != nil {
		s.updateHook(documentID, update)
	}
	return &value, nil
}

func (s *paperInfoPaperServiceStub) SearchMetadata(
	_ context.Context, search types.PaperSearch,
) (types.PaperMetadataPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := types.PaperMetadataPage{
		Page:     search.Page,
		PageSize: search.PageSize,
		Items:    []types.PaperMetadata{},
	}
	title := strings.ToLower(strings.TrimSpace(search.Title))
	for _, metadata := range s.metadata {
		if title != "" && !strings.Contains(strings.ToLower(metadata.Title), title) {
			continue
		}
		result.Items = append(result.Items, metadata)
	}
	result.Total = int64(len(result.Items))
	return result, nil
}

func (*paperInfoPaperServiceStub) AddCitation(
	context.Context, string, types.PaperCitationInput,
) (*types.PaperCitation, error) {
	return nil, errors.New("not implemented")
}

func (s *paperInfoPaperServiceStub) ReplaceCitations(
	_ context.Context,
	documentID string,
	inputs []types.PaperCitationInput,
) ([]types.PaperCitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replacements[documentID] = append([]types.PaperCitationInput(nil), inputs...)
	if s.replaceHook != nil {
		s.replaceHook(documentID, inputs)
	}
	return nil, nil
}

func (s *paperInfoPaperServiceStub) MergeExtractedCitations(
	_ context.Context,
	documentID string,
	inputs []types.PaperCitationInput,
) ([]types.PaperCitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merges[documentID] = append([]types.PaperCitationInput(nil), inputs...)
	return nil, nil
}

func (*paperInfoPaperServiceStub) RemoveCitation(
	context.Context, string, string,
) error {
	return errors.New("not implemented")
}

func (s *paperInfoPaperServiceStub) ListCitations(
	_ context.Context, documentID string,
) ([]types.PaperCitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.PaperCitation(nil), s.citations[documentID]...), nil
}
func (s *paperInfoPaperServiceStub) ListCitationsPage(
	ctx context.Context, documentID string, offset, limit int,
) (types.PaperCitationPage, error) {
	items, err := s.ListCitations(ctx, documentID)
	if err != nil {
		return types.PaperCitationPage{}, err
	}
	if offset >= len(items) {
		return types.PaperCitationPage{Items: []types.PaperCitation{}}, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return types.PaperCitationPage{Items: items[offset:end], HasMore: end < len(items), NextOffset: end}, nil
}

func (s *paperInfoPaperServiceStub) ListCitedBy(
	_ context.Context, documentID string,
) ([]types.PaperCitation, error) {
	targetID := uuid.MustParse(documentID)
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]types.PaperCitation, 0)
	for _, citations := range s.citations {
		for _, citation := range citations {
			if citation.CitedDocumentID == targetID {
				result = append(result, citation)
			}
		}
	}
	return result, nil
}

func (s *paperInfoPaperServiceStub) ResolveCitation(
	_ context.Context,
	citingDocumentID, resolvedDocumentID, rawReference string,
) (*types.PaperCitation, error) {
	citingID := uuid.MustParse(citingDocumentID)
	resolvedID := uuid.MustParse(resolvedDocumentID)
	citation := types.PaperCitation{
		CitingDocumentID: citingID,
		CitedDocumentID:  resolvedID,
		RawReference:     rawReference,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolutions = append(s.resolutions, citation)
	return &citation, nil
}

func TestPaperInfoExtractMetadataPersistsCurrentSnapshot(t *testing.T) {
	documentID := uuid.New()
	markdown := []byte("# A Reliable Paper\n\nAda Lovelace\n")
	documents := newPaperDocumentRepoStub(types.Document{
		ID:          documentID,
		SourceState: types.DocumentSourceAvailable,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID,
		StorageKey: "markdown/test.md",
	}
	documents.blocks[documentID] = []types.Block{{
		ID:         uuid.New(),
		DocumentID: documentID,
		Ordinal:    0,
		Type:       "title",
		Text:       "A Reliable Paper",
	}}
	papers := newPaperInfoPaperServiceStub()
	extractor := &paperInfoExtractorStub{
		extract: func(request *types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			if !bytes.Equal(request.Markdown, markdown) || len(request.Blocks) != 1 {
				t.Fatalf("unexpected extraction request: %#v", request)
			}
			return &types.PaperExtractionResult{
				Title: &types.PaperTextCandidate{
					Value:      " A Reliable Paper ",
					Confidence: 0.95,
				},
				Authors: []types.PaperTextCandidate{{
					Value:      "Ada Lovelace",
					Confidence: 0.9,
				}},
				Abstract: &types.PaperTextCandidate{
					Value:      "A compact\nabstract.",
					Confidence: 0.8,
				},
				ReferencesDetected: true,
				References:         []string{"Reference One", "Reference One", "Reference Two"},
				ExtractorName:      "rules",
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 2},
		documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/test.md": markdown}},
		extractor,
		&paperInfoRegistryStub{},
		papers,
		nil,
		nil,
		nil,
	)

	resolvable, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{
		DocumentID: documentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resolvable {
		t.Fatal("expected extracted title to make catalog resolution usable")
	}
	if len(papers.updates) != 1 ||
		papers.updates[0].update.Title == nil ||
		*papers.updates[0].update.Title != "A Reliable Paper" {
		t.Fatalf("unexpected metadata writes: %#v", papers.updates)
	}
	if got := papers.updates[0].update.Authors; got == nil ||
		len(*got) != 1 ||
		(*got)[0].Name != "Ada Lovelace" {
		t.Fatalf("unexpected author update: %#v", got)
	}
	if got := papers.updates[0].update.Abstract; got == nil ||
		*got != "A compact abstract." {
		t.Fatalf("unexpected abstract update: %#v", got)
	}
	if got := papers.updates[0].update.MetadataSource; got == nil ||
		*got != types.PaperMetadataSourceExtraction {
		t.Fatalf("metadata source = %v, want extraction", got)
	}
	if got := papers.merges[documentID.String()]; len(got) != 2 {
		t.Fatalf("reference merge count = %d, want 2", len(got))
	}
}

func TestPaperInfoExtractMetadataQueuesIndependentDownstreamStages(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		MetadataExtractionStatus: types.DocumentStageQueued, MetadataExtractionGeneration: 3,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/test.md"}
	papers := newPaperInfoPaperServiceStub()
	queue := &paperInfoQueueStub{}
	service := newPaperInfoServiceForTest(
		config.Config{}, documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/test.md": []byte("# Paper\n")}},
		&paperInfoExtractorStub{extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			return &types.PaperExtractionResult{
				Title:              &types.PaperTextCandidate{Value: "Paper", Confidence: 1},
				ReferencesDetected: true, ExtractorName: "test",
			}, nil
		}},
		&paperInfoRegistryStub{}, papers, queue, nil, nil,
	)

	_, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{
		DocumentID: documentID, Stage: types.DocumentStageMetadataExtraction, Generation: 3,
	})
	if err != nil {
		t.Fatalf("ExtractMetadata() error = %v", err)
	}
	document := documents.documents[documentID]
	if document.MetadataExtractionStatus != types.DocumentStageSucceeded ||
		document.MetadataExtractionGeneration != 3 {
		t.Fatalf("metadata stage = %s generation=%d", document.MetadataExtractionStatus, document.MetadataExtractionGeneration)
	}
	if document.SourceLookupStatus != types.DocumentStageQueued || document.SourceLookupGeneration != 1 ||
		document.ReferenceLookupStatus != types.DocumentStageQueued || document.ReferenceLookupGeneration != 1 {
		t.Fatalf("downstream stages were not queued: %#v", document)
	}
	if len(queue.tasks) != 2 {
		t.Fatalf("queued task count = %d, want 2", len(queue.tasks))
	}
	var source types.PaperCatalogResolvePayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &source); err != nil {
		t.Fatal(err)
	}
	var references types.PaperReferenceDispatchPayload
	if err := json.Unmarshal(queue.tasks[1].Payload(), &references); err != nil {
		t.Fatal(err)
	}
	if source.Stage != types.DocumentStageSourceLookup || source.Generation != 1 ||
		references.Stage != types.DocumentStageReferenceLookup || references.Generation != 1 {
		t.Fatalf("downstream payloads = source:%#v references:%#v", source, references)
	}
}

func TestPaperInfoExtractMetadataDoesNotClearExistingMetadataForEmptyResult(t *testing.T) {
	documentID := uuid.New()
	markdown := []byte("# Existing paper\n")
	documents := newPaperDocumentRepoStub(types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/existing.md"}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID:     documentID,
		Title:          "Manual title",
		Abstract:       "Manual abstract",
		Authors:        []types.PaperAuthor{{Name: "Manual author"}},
		MetadataSource: types.PaperMetadataSourceManual,
	}
	service := newPaperInfoServiceForTest(
		config.Config{}, documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/existing.md": markdown}},
		&paperInfoExtractorStub{extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			return &types.PaperExtractionResult{ExtractorName: "rules"}, nil
		}},
		&paperInfoRegistryStub{}, papers, nil, nil, nil,
	)

	resolvable, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{DocumentID: documentID})
	if err != nil {
		t.Fatalf("ExtractMetadata() error = %v", err)
	}
	if !resolvable {
		t.Fatal("persisted title should remain usable for catalog lookup")
	}
	if len(papers.updates) != 0 {
		t.Fatalf("empty extraction wrote metadata patch: %#v", papers.updates)
	}
	if _, merged := papers.merges[documentID.String()]; merged {
		t.Fatal("undetected bibliography must not mutate existing citations")
	}
	got := papers.metadata[documentID.String()]
	if got.Title != "Manual title" || got.Abstract != "Manual abstract" || len(got.Authors) != 1 {
		t.Fatalf("existing metadata was changed: %#v", got)
	}
}

func TestPaperInfoExtractMetadataClearsOnlyAutomaticCitationsForDetectedEmptyBibliography(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/empty-refs.md"}
	papers := newPaperInfoPaperServiceStub()
	service := newPaperInfoServiceForTest(
		config.Config{}, documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/empty-refs.md": []byte("# Paper\n")}},
		&paperInfoExtractorStub{extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			return &types.PaperExtractionResult{
				ExtractorName:      "rules",
				ReferencesDetected: true,
			}, nil
		}},
		&paperInfoRegistryStub{}, papers, nil, nil, nil,
	)

	if _, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{DocumentID: documentID}); err != nil {
		t.Fatalf("ExtractMetadata() error = %v", err)
	}
	inputs, called := papers.merges[documentID.String()]
	if !called || len(inputs) != 0 {
		t.Fatalf("detected empty bibliography merge = %#v, called=%t; want one empty merge", inputs, called)
	}
}

func TestExtractionMetadataUpdateOnlyIncludesUsableCandidates(t *testing.T) {
	result := &types.PaperExtractionResult{
		Title:    &types.PaperTextCandidate{Value: "  ", Confidence: 1},
		Abstract: &types.PaperTextCandidate{Value: "Abstract body", Confidence: 1},
		Authors: []types.PaperTextCandidate{
			{Value: "  ", Confidence: 1},
			{Value: "Ada Lovelace", Confidence: 1},
		},
	}
	update := extractionMetadataUpdate(result)
	if update.Title != nil {
		t.Fatalf("empty title must be omitted: %#v", update)
	}
	if update.Abstract == nil || *update.Abstract != "Abstract body" {
		t.Fatalf("abstract update = %#v", update.Abstract)
	}
	if update.Authors == nil || len(*update.Authors) != 1 || (*update.Authors)[0].Name != "Ada Lovelace" {
		t.Fatalf("authors update = %#v", update.Authors)
	}
	if update.MetadataSource == nil || *update.MetadataSource != types.PaperMetadataSourceExtraction {
		t.Fatalf("metadata source = %#v, want extraction", update.MetadataSource)
	}
}

func TestCatalogMetadataUpdateOmitsEmptyAutomaticFields(t *testing.T) {
	update := catalogMetadataUpdate(types.PaperCatalogCandidate{Provider: "test", Title: "Catalog title"})
	if update.Title == nil || *update.Title != "Catalog title" {
		t.Fatalf("catalog title update = %#v", update.Title)
	}
	if update.Abstract != nil || update.Authors != nil || update.Tags != nil || update.Venue != nil {
		t.Fatalf("empty catalog fields must be omitted: %#v", update)
	}
	if update.MetadataSource == nil || *update.MetadataSource != types.PaperMetadataSourceCatalog {
		t.Fatalf("metadata source = %#v, want catalog", update.MetadataSource)
	}
}

func TestPaperInfoExtractMetadataRejectsSourceDeletingDuringRPC(t *testing.T) {
	documentID := uuid.New()
	markdown := []byte("# Original\n")
	documents := newPaperDocumentRepoStub(types.Document{
		ID:          documentID,
		SourceState: types.DocumentSourceAvailable,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID,
		StorageKey: "markdown/original.md",
	}
	papers := newPaperInfoPaperServiceStub()
	extractor := &paperInfoExtractorStub{
		extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			document := documents.documents[documentID]
			document.SourceState = types.DocumentSourceDeleting
			documents.documents[documentID] = document
			return &types.PaperExtractionResult{
				Title: &types.PaperTextCandidate{
					Value:      "Stale title",
					Confidence: 1,
				},
				ExtractorName: "rules",
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/original.md": markdown}},
		extractor,
		&paperInfoRegistryStub{},
		papers,
		nil,
		nil,
		nil,
	)

	_, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{
		DocumentID: documentID,
	})
	if !errors.Is(err, ErrStalePaperInfo) {
		t.Fatalf("expected stale revision error, got %v", err)
	}
	if len(papers.updates) != 0 || len(papers.replacements) != 0 {
		t.Fatalf("stale extraction wrote data: updates=%d replacements=%d",
			len(papers.updates), len(papers.replacements))
	}
}

func TestPaperInfoExtractMetadataRejectsCancelledGenerationAfterRPC(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		MetadataExtractionStatus: types.DocumentStageQueued, MetadataExtractionGeneration: 1,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/original.md"}
	papers := newPaperInfoPaperServiceStub()
	extractor := &paperInfoExtractorStub{extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
		if _, applied, err := documents.CancelStage(context.Background(), documentID,
			types.DocumentStageMetadataExtraction, 1); err != nil || !applied {
			t.Fatalf("cancel metadata stage applied=%t error=%v", applied, err)
		}
		return &types.PaperExtractionResult{
			Title: &types.PaperTextCandidate{Value: "Late title", Confidence: 1}, ExtractorName: "test",
		}, nil
	}}
	service := newPaperInfoServiceForTest(
		config.Config{}, documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/original.md": []byte("# Original\n")}},
		extractor, &paperInfoRegistryStub{}, papers, nil, nil, nil,
	)

	_, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{
		DocumentID: documentID, Stage: types.DocumentStageMetadataExtraction, Generation: 1,
	})
	if !errors.Is(err, asynq.RevokeTask) {
		t.Fatalf("ExtractMetadata() error = %v, want RevokeTask", err)
	}
	if len(papers.updates) != 0 || len(papers.merges) != 0 {
		t.Fatalf("cancelled generation wrote data: updates=%d merges=%d", len(papers.updates), len(papers.merges))
	}
}

func TestPaperInfoResolveMetadataBoundsProvidersAndRanksExactDOI(t *testing.T) {
	documentID := uuid.New()
	documents := readyPaperInfoDocumentRepo(documentID)
	documents.documents[documentID] = types.Document{
		ID:          documentID,
		DOI:         "10.1000/exact",
		SourceState: types.DocumentSourceAvailable,
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID,
		Title:      "A Reliable Paper",
	}

	var active atomic.Int32
	var maximum atomic.Int32
	provider := func(name string, candidate types.PaperCatalogCandidate) interfaces.PaperCatalogProvider {
		return &paperInfoProviderStub{
			name: name,
			search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
				now := active.Add(1)
				for {
					previous := maximum.Load()
					if now <= previous || maximum.CompareAndSwap(previous, now) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				active.Add(-1)
				return []types.PaperCatalogCandidate{candidate}, nil
			},
		}
	}
	registry := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		provider("arxiv", testCatalogCandidate("arxiv", "wrong", "Wrong title", 0.99)),
		provider("crossref", testCatalogCandidate("crossref", "10.1000/exact", "Exact DOI title", 0.1)),
		provider("openalex", testCatalogCandidate("openalex", "", "A Reliable Paper", 1)),
	}}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 2},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		registry,
		papers,
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if maximum.Load() < 2 || maximum.Load() > 2 {
		t.Fatalf("maximum provider concurrency = %d, want 2", maximum.Load())
	}
	if len(papers.updates) != 1 ||
		papers.updates[0].update.Title == nil ||
		*papers.updates[0].update.Title != "Exact DOI title" {
		t.Fatalf("exact DOI candidate was not selected: %#v", papers.updates)
	}
}

func TestProviderSpansDistinguishConcurrentReferenceTargets(t *testing.T) {
	sourceID, firstReferenceID, secondReferenceID := uuid.New(), uuid.New(), uuid.New()
	spans := &paperInfoSpanStub{}
	service := newPaperInfoService(
		config.Config{PaperCatalogConcurrency: 1},
		nil,
		nil,
		nil,
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
			&paperInfoProviderStub{name: "openalex", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
				return []types.PaperCatalogCandidate{testCatalogCandidate("openalex", "10.1000/example", "Example Paper", 1)}, nil
			}},
		}},
		nil,
		nil,
		nil,
		nil,
		spans,
	).(*paperInfoService)
	query := types.PaperLookupQuery{Title: "Example Paper"}
	for _, targetID := range []uuid.UUID{firstReferenceID, secondReferenceID} {
		if _, _, err := service.selectCatalogCandidate(
			context.Background(),
			query,
			sourceID,
			1,
			types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogReference, targetID.String()),
			uuid.Nil,
		); err != nil {
			t.Fatalf("selectCatalogCandidate() error = %v", err)
		}
	}
	if len(spans.starts) != 2 {
		t.Fatalf("provider span starts = %d, want 2", len(spans.starts))
	}
	first, second := spans.starts[0], spans.starts[1]
	if first.scope != types.ProcessingSpanScopeProvider || second.scope != types.ProcessingSpanScopeProvider || first.scopeID == second.scopeID {
		t.Fatalf("provider span scopes = %#v, %#v; reference targets must not share a span key", first, second)
	}
	if want := types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogProvider, types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogReference, firstReferenceID.String()).String(), types.PaperCatalogProviderOpenAlex.String()); first.scopeID != want {
		t.Fatalf("first provider scope id = %q, want %q", first.scopeID, want)
	}
	if want := types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogProvider, types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogReference, secondReferenceID.String()).String(), types.PaperCatalogProviderOpenAlex.String()); second.scopeID != want {
		t.Fatalf("second provider scope id = %q, want %q", second.scopeID, want)
	}
}

func TestPaperInfoResolveMetadataRepointsMatchedCitation(t *testing.T) {
	documentID := uuid.New()
	placeholderID := uuid.New()
	resolvedID := uuid.New()
	documents := readyPaperInfoDocumentRepo(documentID)
	documents.documents[placeholderID] = types.Document{
		ID:          placeholderID,
		SourceState: types.DocumentSourceMissing,
	}
	documents.documents[resolvedID] = types.Document{
		ID:          resolvedID,
		DOI:         "10.2000/cited",
		SourceState: types.DocumentSourceAvailable,
	}
	documents.search = func(search types.DocumentSearch) (types.DocumentPage, error) {
		if search.DOI == "10.2000/cited" {
			return types.DocumentPage{Items: []types.Document{documents.documents[resolvedID]}}, nil
		}
		return types.DocumentPage{}, nil
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID,
		Title:      "Source Paper",
	}
	rawReference := "Ada Lovelace. 2020. Cited Work. Journal."
	papers.citations[documentID.String()] = []types.PaperCitation{{
		CitingDocumentID: documentID,
		CitedDocumentID:  placeholderID,
		RawReference:     rawReference,
	}}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			if query.Title == "Cited Work" {
				return []types.PaperCatalogCandidate{
					testCatalogCandidate("crossref", "10.2000/cited", "Cited Work", 1),
				}, nil
			}
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.2000/source", "Source Paper", 1),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reference resolution is independent low-priority work. The source task
	// must not synchronously inspect an entire bibliography.
	registerReferenceJobForTest(t, service, placeholderID, documentID)
	err = service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: placeholderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := documents.Get(context.Background(), placeholderID)
	if merged.SourceState != types.DocumentSourceMerged || merged.CanonicalDocumentID == nil || *merged.CanonicalDocumentID != resolvedID {
		t.Fatalf("unexpected merged target: %#v", merged)
	}
}

func TestPaperInfoReferenceDispatcherQueuesBoundedDeduplicatedReferences(t *testing.T) {
	documentID := uuid.New()
	first := uuid.New()
	second := uuid.New()
	duplicate := uuid.New()
	documents := readyPaperInfoDocumentRepo(documentID)
	for _, id := range []uuid.UUID{first, second, duplicate} {
		documents.documents[id] = types.Document{ID: id, SourceState: types.DocumentSourceMissing}
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	// Placeholders created from Python extraction candidates carry the
	// extraction metadata source; only those join the automatic catalog run.
	for _, id := range []uuid.UUID{first, second, duplicate} {
		papers.metadata[id.String()] = types.PaperMetadata{DocumentID: id, MetadataSource: types.PaperMetadataSourceExtraction}
	}
	// A manually added unknown reference must never be auto-dispatched.
	manual := uuid.New()
	documents.documents[manual] = types.Document{ID: manual, SourceState: types.DocumentSourceMissing}
	papers.metadata[manual.String()] = types.PaperMetadata{DocumentID: manual, MetadataSource: types.PaperMetadataSourceManual}
	papers.citations[documentID.String()] = []types.PaperCitation{
		{CitingDocumentID: documentID, CitedDocumentID: first, RawReference: "Ada. 2024. One. doi:10.1000/one"},
		{CitingDocumentID: documentID, CitedDocumentID: duplicate, RawReference: "Other rendering. doi:10.1000/one"},
		{CitingDocumentID: documentID, CitedDocumentID: second, RawReference: "Grace. 2023. Two. doi:10.1000/two"},
		{CitingDocumentID: documentID, CitedDocumentID: manual, RawReference: "Manual entry. doi:10.1000/three"},
	}
	queue := &paperInfoQueueStub{}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1, PaperReferenceCatalogMaxPerDocument: 2}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
		return []types.PaperCatalogCandidate{testCatalogCandidate("crossref", "10.1000/source", "Source Paper", 1)}, nil
	}}}}, papers, queue, nil, nil)
	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{DocumentID: documentID}); err != nil {
		t.Fatal(err)
	}
	if len(queue.tasks) != 0 {
		t.Fatalf("source catalog task queued reference work = %d, want 0", len(queue.tasks))
	}
	if err := service.DispatchReferences(context.Background(), types.PaperReferenceDispatchCommand{
		DocumentID: documentID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(queue.tasks) != 2 {
		t.Fatalf("reference tasks = %d, want two deduplicated targets", len(queue.tasks))
	}
	seen := map[string]bool{}
	for _, task := range queue.tasks {
		var payload types.PaperCatalogResolvePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		seen[payload.DocumentID] = true
	}
	if !seen[first.String()] || !seen[second.String()] || seen[duplicate.String()] {
		t.Fatalf("deduplicated references = %#v", seen)
	}
}

func TestPaperInfoReferenceDispatcherSharesTargetGenerationAcrossSources(t *testing.T) {
	firstSource, secondSource, target := uuid.New(), uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: firstSource, SourceState: types.DocumentSourceAvailable,
			ReferenceLookupStatus: types.DocumentStageQueued, ReferenceLookupGeneration: 1},
		types.Document{ID: secondSource, SourceState: types.DocumentSourceAvailable,
			ReferenceLookupStatus: types.DocumentStageQueued, ReferenceLookupGeneration: 1},
		types.Document{ID: target, SourceState: types.DocumentSourceMissing},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[target.String()] = types.PaperMetadata{
		DocumentID: target, MetadataSource: types.PaperMetadataSourceExtraction,
	}
	for _, sourceID := range []uuid.UUID{firstSource, secondSource} {
		papers.metadata[sourceID.String()] = types.PaperMetadata{DocumentID: sourceID}
		papers.citations[sourceID.String()] = []types.PaperCitation{{
			CitingDocumentID: sourceID, CitedDocumentID: target, RawReference: "Shared target",
		}}
	}
	queue := &paperInfoQueueStub{}
	service := newPaperInfoServiceForTest(config.Config{}, documents, nil, nil,
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref"}}},
		papers, queue, nil, nil)

	for _, sourceID := range []uuid.UUID{firstSource, secondSource} {
		if err := service.DispatchReferences(context.Background(), types.PaperReferenceDispatchCommand{
			DocumentID: sourceID, Stage: types.DocumentStageReferenceLookup, Generation: 1,
		}); err != nil {
			t.Fatalf("DispatchReferences(%s): %v", sourceID, err)
		}
	}
	if len(queue.tasks) != 1 {
		t.Fatalf("target tasks = %d, want one", len(queue.tasks))
	}
	targetState, err := documents.GetStage(context.Background(), target, types.DocumentStageSourceLookup)
	if err != nil || targetState.Status != types.DocumentStageQueued || targetState.Generation != 1 {
		t.Fatalf("shared target stage = %#v, error %v", targetState, err)
	}
	for _, sourceID := range []uuid.UUID{firstSource, secondSource} {
		sourceState, stateErr := documents.GetStage(context.Background(), sourceID, types.DocumentStageReferenceLookup)
		if stateErr != nil || sourceState.Status != types.DocumentStageSucceeded {
			t.Fatalf("source dispatch stage = %#v, error %v", sourceState, stateErr)
		}
	}

	implementation := service.(*paperInfoService)
	first := documents.documents[firstSource]
	first.ReferenceLookupStatus = types.DocumentStageQueued
	first.ReferenceLookupGeneration = 2
	documents.documents[firstSource] = first
	if err := implementation.Cancel(context.Background(), firstSource.String()); err != nil {
		t.Fatalf("Cancel(first source): %v", err)
	}
	targetAfterCancel, err := documents.GetStage(context.Background(), target, types.DocumentStageSourceLookup)
	if err != nil || targetAfterCancel.Status != types.DocumentStageQueued || targetAfterCancel.Generation != 1 {
		t.Fatalf("source cancellation changed target stage = %#v, error %v", targetAfterCancel, err)
	}
}

func TestPaperInfoReferenceTaskCarriesOnlyTarget(t *testing.T) {
	payload := types.PaperCatalogResolvePayload{DocumentID: uuid.NewString(), Stage: types.DocumentStageSourceLookup, Generation: 1}
	command, err := payload.Command()
	if err != nil || command.DocumentID.String() != payload.DocumentID {
		t.Fatalf("single-target reference command = %#v, err=%v", command, err)
	}
}

func TestPaperInfoResolveSourceMergesDOIConflictIntoCanonical(t *testing.T) {
	documentID := uuid.New()
	otherID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: otherID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/owned"},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	provider := &paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
		return []types.PaperCatalogCandidate{testCatalogCandidate("crossref", "10.1000/owned", "Source Paper", 1)}, nil
	}}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}}, papers, nil, nil, nil)
	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{DocumentID: documentID}); err != nil {
		t.Fatal(err)
	}
	merged := documents.documents[documentID]
	if merged.SourceState != types.DocumentSourceMerged || merged.CanonicalDocumentID == nil || *merged.CanonicalDocumentID != otherID {
		t.Fatalf("DOI conflict loser = %#v", merged)
	}
	if len(papers.updates) != 0 {
		t.Fatalf("canonicalized loser received metadata updates=%d", len(papers.updates))
	}
}

func TestPaperInfoResolveMetadataRejectsStatusChangedDuringLookup(t *testing.T) {
	documentID := uuid.New()
	documents := readyPaperInfoDocumentRepo(documentID)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID,
		Title:      "Original",
	}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			document := documents.documents[documentID]
			document.SourceState = types.DocumentSourceDeleting
			document.SourceState = types.DocumentSourceDeleting
			documents.documents[documentID] = document
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.1000/new", "Original", 1),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID,
	})
	if !errors.Is(err, ErrStalePaperInfo) {
		t.Fatalf("expected stale revision error, got %v", err)
	}
	if len(papers.updates) != 0 {
		t.Fatalf("stale catalog lookup wrote metadata: %#v", papers.updates)
	}
}

func TestPaperInfoResolveMetadataRejectsDOIChangedDuringLookup(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID:          documentID,
		SourceState: types.DocumentSourceAvailable,
		DOI:         "10.1000/original",
	})
	papers := newPaperInfoPaperServiceStub()
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			document := documents.documents[documentID]
			document.DOI = "10.1000/replacement"
			documents.documents[documentID] = document
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.1000/original", "Original", 1),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID,
	})
	if !errors.Is(err, ErrStalePaperInfo) {
		t.Fatalf("expected stale DOI error, got %v", err)
	}
	if len(papers.updates) != 0 {
		t.Fatalf("stale DOI lookup wrote metadata: %#v", papers.updates)
	}
}

func TestPaperInfoResolveUnknownReferenceWithoutMarkdown(t *testing.T) {
	citingID := uuid.New()
	unknownID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: unknownID, SourceState: types.DocumentSourceMissing},
	)
	papers := newPaperInfoPaperServiceStub()
	rawReference := "Ada Lovelace. 2024. Source-Free Paper. Journal. doi:10.3000/source-free"
	papers.citations[citingID.String()] = []types.PaperCitation{{
		CitingDocumentID: citingID,
		CitedDocumentID:  unknownID,
		RawReference:     rawReference,
	}}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			if query.DOI != "10.3000/source-free" {
				t.Fatalf("DOI query = %q, want incoming reference DOI", query.DOI)
			}
			return []types.PaperCatalogCandidate{
				testCatalogCandidate(
					"crossref",
					"10.3000/source-free",
					"Source-Free Paper",
					1,
				),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	registerReferenceJobForTest(t, service, unknownID, citingID)
	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: unknownID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers.updates) != 1 ||
		papers.updates[0].documentID != unknownID.String() ||
		papers.updates[0].update.Title == nil ||
		*papers.updates[0].update.Title != "Source-Free Paper" {
		t.Fatalf("unknown paper metadata was not populated: %#v", papers.updates)
	}
}

func TestPaperInfoResolveUnknownReferenceNeverQueuesPDFDownload(t *testing.T) {
	citingID, targetID := uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: targetID, SourceState: types.DocumentSourceMissing},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.citations[citingID.String()] = []types.PaperCitation{{CitingDocumentID: citingID, CitedDocumentID: targetID, RawReference: "Ada. Downloadable Paper. doi:10.3000/downloadable"}}
	provider := &paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
		candidate := testCatalogCandidate("crossref", "10.3000/downloadable", "Downloadable Paper", 1)
		candidate.SourceURL = "https://example.test/paper.pdf"
		return []types.PaperCatalogCandidate{candidate}, nil
	}}
	queue := &paperInfoQueueStub{}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}}, papers, queue, nil, nil)
	registerReferenceJobForTest(t, service, targetID, citingID)
	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{DocumentID: targetID}); err != nil {
		t.Fatal(err)
	}
	if len(queue.tasks) != 0 {
		t.Fatalf("reference lookup queued unexpected tasks: %#v", queue.tasks)
	}
}

func TestPaperInfoResolveUnknownReferencePrefersStoredExtractionMetadata(t *testing.T) {
	citingID := uuid.New()
	unknownID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, SourceState: types.DocumentSourceAvailable},
		types.Document{
			ID:          unknownID,
			DOI:         "10.4000/structured-reference",
			SourceState: types.DocumentSourceMissing,
		},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[unknownID.String()] = types.PaperMetadata{
		DocumentID: unknownID,
		Title:      "Structured Reference Title",
		Authors:    []types.PaperAuthor{{Name: "Ada Lovelace"}},
		Venue:      "Structured Journal",
		PublishedAt: func() *time.Time {
			value := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
			return &value
		}(),
	}
	// This deliberately conflicts with the structured extraction result. The
	// scheduler must not reparse it while the placeholder has a valid identity.
	papers.citations[citingID.String()] = []types.PaperCitation{{
		CitingDocumentID: citingID,
		CitedDocumentID:  unknownID,
		RawReference:     "Different Raw Title. 2020. doi:10.9999/raw-reference",
	}}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			if query.DOI != "10.4000/structured-reference" || query.Title != "Structured Reference Title" {
				t.Fatalf("query = %#v, want stored extraction identity", query)
			}
			if len(query.Authors) != 1 || query.Authors[0] != "Ada Lovelace" || query.PublishedYear != 2024 {
				t.Fatalf("query did not retain stored reference context: %#v", query)
			}
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.4000/structured-reference", "Structured Reference Title", 1),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	registerReferenceJobForTest(t, service, unknownID, citingID)
	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: unknownID,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPaperArXivIDAcceptsBareAndWrappedIDsWithoutDamagingLegacyIDs(t *testing.T) {
	for input, want := range map[string]string{
		"2401.01234v2":                       "2401.01234",
		" arXiv 2401.01234v2 ":               "2401.01234",
		"https://arxiv.org/abs/2401.01234v2": "2401.01234",
		"hep-th/9901001":                     "hep-th/9901001",
	} {
		if got := paperArXivID(input); got != want {
			t.Fatalf("paperArXivID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPaperInfoResolveUnknownReferenceRequiresRegisteredJob(t *testing.T) {
	unknownID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: unknownID, SourceState: types.DocumentSourceMissing,
	})
	papers := newPaperInfoPaperServiceStub()
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{},
		papers,
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: unknownID,
	})
	if err == nil {
		t.Fatal("unregistered reference job unexpectedly resolved")
	}
	if len(papers.updates) != 0 {
		t.Fatalf("unregistered reference job updated metadata: %#v", papers.updates)
	}
}

func TestPaperInfoResolveUnknownRequiresRegisteredTarget(t *testing.T) {
	unknownID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: unknownID, SourceState: types.DocumentSourceMissing,
	})
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{},
		newPaperInfoPaperServiceStub(),
		nil,
		nil,
		nil,
	)
	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{DocumentID: unknownID})
	if err == nil {
		t.Fatal("unregistered target unexpectedly resolved")
	}
}

func TestPaperInfoResolveUnknownReferenceRepointsCitationToStoredPaper(t *testing.T) {
	citingID := uuid.New()
	unknownID := uuid.New()
	storedID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: unknownID, SourceState: types.DocumentSourceMissing},
		types.Document{
			ID:          storedID,
			DOI:         "10.4000/stored",
			SourceState: types.DocumentSourceAvailable,
		},
	)
	documents.search = func(search types.DocumentSearch) (types.DocumentPage, error) {
		if search.DOI == "10.4000/stored" {
			return types.DocumentPage{
				Items: []types.Document{documents.documents[storedID]},
			}, nil
		}
		return types.DocumentPage{}, nil
	}
	papers := newPaperInfoPaperServiceStub()
	rawReference := "Grace Hopper. 2023. Stored Paper. doi:10.4000/stored"
	papers.citations[citingID.String()] = []types.PaperCitation{{
		CitingDocumentID: citingID,
		CitedDocumentID:  unknownID,
		RawReference:     rawReference,
	}}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.4000/stored", "Stored Paper", 1),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	registerReferenceJobForTest(t, service, unknownID, citingID)
	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: unknownID,
	})
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := documents.Get(context.Background(), unknownID)
	if merged.SourceState != types.DocumentSourceMerged || merged.CanonicalDocumentID == nil || *merged.CanonicalDocumentID != storedID {
		t.Fatalf("incoming target was not canonicalized: %#v", merged)
	}
	if len(papers.updates) != 0 {
		t.Fatalf("matched local paper should not update placeholder: %#v", papers.updates)
	}
}

func TestPaperInfoResolveMetadataAllowsProcessingDocumentWithoutMarkdown(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID:          documentID,
		SourceState: types.DocumentSourceAvailable,
	})
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID,
		Title:      "Concurrent Paper Lookup",
	}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			if query.Title != "Concurrent Paper Lookup" {
				t.Fatalf("query title = %q", query.Title)
			}
			return []types.PaperCatalogCandidate{
				testCatalogCandidate(
					"crossref",
					"10.1000/concurrent",
					"Concurrent Paper Lookup",
					1,
				),
			}, nil
		},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers.updates) != 1 {
		t.Fatalf("metadata writes = %d, want 1", len(papers.updates))
	}
	if source := papers.updates[0].update.MetadataSource; source == nil ||
		*source != types.PaperMetadataSourceCatalog {
		t.Fatalf("metadata source = %v, want catalog", source)
	}
}

func TestPaperInfoResolveMetadataRejectsDeletingDocument(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID:          documentID,
		SourceState: types.DocumentSourceDeleting,
	})
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{},
		newPaperInfoPaperServiceStub(),
		nil,
		nil,
		nil,
	)

	err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID,
	})
	if !errors.Is(err, ErrStalePaperInfo) {
		t.Fatalf("expected deleting document rejection, got %v", err)
	}
}

func TestScorePaperCatalogCandidateUsesBibliographicSignals(t *testing.T) {
	publishedAt := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	query := types.PaperLookupQuery{
		Title:         "Reliable Document Understanding",
		Authors:       []string{"Ada Lovelace"},
		PublishedYear: 2024,
		Venue:         "Document Intelligence Conference",
	}
	matching := types.PaperCatalogCandidate{
		Title:       "Reliable Document Understanding",
		Authors:     []types.PaperAuthor{{Name: "Ada Lovelace"}},
		PublishedAt: &publishedAt,
		Venue:       "Document Intelligence Conference",
	}
	unrelated := types.PaperCatalogCandidate{
		Title:       "Unrelated Systems Research",
		Authors:     []types.PaperAuthor{{Name: "Grace Hopper"}},
		PublishedAt: &publishedAt,
		Venue:       "Another Venue",
	}

	matchingScore, exact := scorePaperCatalogCandidate(query, matching)
	if exact {
		t.Fatal("non-identifier match should not be exact")
	}
	unrelatedScore, _ := scorePaperCatalogCandidate(query, unrelated)
	if matchingScore <= unrelatedScore || matchingScore < minPaperCatalogMatchScore {
		t.Fatalf("matching score %f should beat unrelated score %f and threshold %f",
			matchingScore, unrelatedScore, minPaperCatalogMatchScore)
	}
}

func TestScorePaperCatalogCandidateDoesNotPenalizeDifferentDOI(t *testing.T) {
	query := types.PaperLookupQuery{
		DOI:   "10.1000/query-record",
		Title: "Reliable Document Understanding",
	}
	withoutDOI := types.PaperCatalogCandidate{
		Title:         query.Title,
		ProviderScore: 0.8,
	}
	withDifferentDOI := withoutDOI
	withDifferentDOI.DOI = "10.2000/provider-record"

	withoutDOIScore, withoutDOIExact := scorePaperCatalogCandidate(query, withoutDOI)
	differentDOIScore, differentDOIExact := scorePaperCatalogCandidate(query, withDifferentDOI)
	if withoutDOIExact || differentDOIExact {
		t.Fatal("different or absent candidate DOI must not be treated as an exact match")
	}
	if math.Abs(withoutDOIScore-differentDOIScore) > 1e-9 {
		t.Fatalf(
			"different provider DOI must not reduce a title match: without DOI=%f, different DOI=%f",
			withoutDOIScore,
			differentDOIScore,
		)
	}
}

func TestScorePaperCatalogCandidateMakesTitleDominant(t *testing.T) {
	query := types.PaperLookupQuery{
		Title:   "Reliable Document Understanding",
		Authors: []string{"Ada Lovelace"},
	}
	titleMatch := types.PaperCatalogCandidate{
		Title:   query.Title,
		Authors: []types.PaperAuthor{{Name: "Grace Hopper"}},
	}
	authorMatch := types.PaperCatalogCandidate{
		Title:   "Unrelated Systems Research",
		Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}},
	}

	titleScore, _ := scorePaperCatalogCandidate(query, titleMatch)
	authorScore, _ := scorePaperCatalogCandidate(query, authorMatch)
	if titleScore <= authorScore {
		t.Fatalf("title match should dominate author-only match: title=%f, author=%f",
			titleScore, authorScore)
	}
}

func TestFindStoredCatalogDocumentFallsBackToNormalizedTitle(t *testing.T) {
	citingDocumentID := uuid.New()
	storedDocumentID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingDocumentID, SourceState: types.DocumentSourceAvailable},
		types.Document{
			ID:          storedDocumentID,
			DOI:         "10.1000/local-doi",
			SourceState: types.DocumentSourceAvailable,
		},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[storedDocumentID.String()] = types.PaperMetadata{
		DocumentID: storedDocumentID,
		Title:      "Reliable Document Understanding: A Study",
		Authors:    []types.PaperAuthor{{Name: "Ada Lovelace"}},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{},
		papers,
		nil,
		nil,
		nil,
	).(*paperInfoService)

	found, err := service.findStoredCatalogDocument(
		context.Background(),
		types.PaperCatalogCandidate{
			DOI:     "10.2000/provider-doi",
			Title:   "Reliable Document Understanding — A Study",
			Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}},
		},
		citingDocumentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if found != storedDocumentID {
		t.Fatalf("matched document = %s, want %s", found, storedDocumentID)
	}
}

func TestFindStoredCatalogDocumentRejectsTitleWithDifferentAuthors(t *testing.T) {
	citingDocumentID := uuid.New()
	storedDocumentID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingDocumentID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: storedDocumentID, SourceState: types.DocumentSourceAvailable},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[storedDocumentID.String()] = types.PaperMetadata{
		DocumentID: storedDocumentID,
		Title:      "A Shared Paper Title",
		Authors:    []types.PaperAuthor{{Name: "Grace Hopper"}},
	}
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{},
		papers,
		nil,
		nil,
		nil,
	).(*paperInfoService)

	found, err := service.findStoredCatalogDocument(
		context.Background(),
		types.PaperCatalogCandidate{
			Title:   "A Shared Paper Title",
			Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}},
		},
		citingDocumentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if found != uuid.Nil {
		t.Fatalf("different-author title matched document %s", found)
	}
}

func readyPaperInfoDocumentRepo(documentID uuid.UUID) *paperDocumentRepoStub {
	documents := newPaperDocumentRepoStub(types.Document{
		ID:          documentID,
		SourceState: types.DocumentSourceAvailable,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID,
	}
	return documents
}

func testCatalogCandidate(
	provider, doi, title string,
	providerScore float64,
) types.PaperCatalogCandidate {
	return types.PaperCatalogCandidate{
		Provider:      provider,
		ProviderID:    provider + "-id",
		DOI:           doi,
		Title:         title,
		Authors:       []types.PaperAuthor{},
		Tags:          []string{},
		ProviderScore: providerScore,
	}
}
