package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

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
	revision := testMarkdownSHA(markdown)
	documents := newPaperDocumentRepoStub(types.Document{
		ID:     documentID,
		Status: types.DocumentStatusReady,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID,
		StorageKey: "markdown/test.md",
		SHA256:     revision,
	}
	documents.blocks[documentID] = []types.Block{{
		ID:             uuid.New(),
		DocumentID:     documentID,
		Ordinal:        0,
		Type:           "title",
		Text:           "A Reliable Paper",
		MarkdownSHA256: revision,
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
				References:    []string{"Reference One", "Reference One", "Reference Two"},
				ExtractorName: "rules",
			}, nil
		},
	}
	service := NewPaperInfoService(
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
		DocumentID:     documentID,
		MarkdownSHA256: revision,
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
	if got := papers.replacements[documentID.String()]; len(got) != 2 {
		t.Fatalf("reference replacement count = %d, want 2", len(got))
	}
}

func TestPaperInfoExtractMetadataRejectsRevisionChangedDuringRPC(t *testing.T) {
	documentID := uuid.New()
	markdown := []byte("# Original\n")
	revision := testMarkdownSHA(markdown)
	documents := newPaperDocumentRepoStub(types.Document{
		ID:     documentID,
		Status: types.DocumentStatusReady,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID,
		StorageKey: "markdown/original.md",
		SHA256:     revision,
	}
	papers := newPaperInfoPaperServiceStub()
	extractor := &paperInfoExtractorStub{
		extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			changed := documents.markdowns[documentID]
			changed.SHA256 = testMarkdownSHA([]byte("# Reparsed\n"))
			documents.markdowns[documentID] = changed
			return &types.PaperExtractionResult{
				Title: &types.PaperTextCandidate{
					Value:      "Stale title",
					Confidence: 1,
				},
				ExtractorName: "rules",
			}, nil
		},
	}
	service := NewPaperInfoService(
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
		DocumentID:     documentID,
		MarkdownSHA256: revision,
	})
	if !errors.Is(err, ErrStalePaperInfo) {
		t.Fatalf("expected stale revision error, got %v", err)
	}
	if len(papers.updates) != 0 || len(papers.replacements) != 0 {
		t.Fatalf("stale extraction wrote data: updates=%d replacements=%d",
			len(papers.updates), len(papers.replacements))
	}
}

func TestPaperInfoResolveMetadataBoundsProvidersAndRanksExactDOI(t *testing.T) {
	documentID := uuid.New()
	revision := testMarkdownSHA([]byte("# Paper\n"))
	documents := readyPaperInfoDocumentRepo(documentID, revision)
	documents.documents[documentID] = types.Document{
		ID:     documentID,
		DOI:    "10.1000/exact",
		Status: types.DocumentStatusReady,
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
	service := NewPaperInfoService(
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
		DocumentID:     documentID,
		MarkdownSHA256: revision,
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

func TestPaperInfoResolveMetadataRepointsMatchedCitation(t *testing.T) {
	documentID := uuid.New()
	placeholderID := uuid.New()
	resolvedID := uuid.New()
	revision := testMarkdownSHA([]byte("# Source\n"))
	documents := readyPaperInfoDocumentRepo(documentID, revision)
	documents.documents[placeholderID] = types.Document{
		ID:     placeholderID,
		Status: types.DocumentStatusUnknown,
	}
	documents.documents[resolvedID] = types.Document{
		ID:     resolvedID,
		DOI:    "10.2000/cited",
		Status: types.DocumentStatusReady,
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
	service := NewPaperInfoService(
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
		DocumentID:     documentID,
		MarkdownSHA256: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reference resolution is independent low-priority work. The source task
	// must not synchronously inspect an entire bibliography.
	err = service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: documentID, MarkdownSHA256: revision,
		Scope: types.PaperCatalogScopeReference, CitedDocumentID: placeholderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers.resolutions) != 1 ||
		papers.resolutions[0].CitedDocumentID != resolvedID ||
		papers.resolutions[0].RawReference != rawReference {
		t.Fatalf("unexpected citation resolutions: %#v", papers.resolutions)
	}
}

func TestPaperInfoResolveSourceQueuesBoundedDeduplicatedReferences(t *testing.T) {
	documentID := uuid.New()
	first := uuid.New()
	second := uuid.New()
	duplicate := uuid.New()
	revision := testMarkdownSHA([]byte("# Source\n"))
	documents := readyPaperInfoDocumentRepo(documentID, revision)
	for _, id := range []uuid.UUID{first, second, duplicate} {
		documents.documents[id] = types.Document{ID: id, Status: types.DocumentStatusUnknown}
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	papers.citations[documentID.String()] = []types.PaperCitation{
		{CitingDocumentID: documentID, CitedDocumentID: first, RawReference: "Ada. 2024. One. doi:10.1000/one"},
		{CitingDocumentID: documentID, CitedDocumentID: duplicate, RawReference: "Other rendering. doi:10.1000/one"},
		{CitingDocumentID: documentID, CitedDocumentID: second, RawReference: "Grace. 2023. Two. doi:10.1000/two"},
	}
	queue := &paperInfoQueueStub{}
	service := NewPaperInfoService(config.Config{PaperCatalogConcurrency: 1, PaperReferenceCatalogMaxPerDocument: 2}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
		return []types.PaperCatalogCandidate{testCatalogCandidate("crossref", "10.1000/source", "Source Paper", 1)}, nil
	}}}}, papers, queue, nil, nil)
	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{DocumentID: documentID, MarkdownSHA256: revision}); err != nil {
		t.Fatal(err)
	}
	if len(queue.tasks) != 2 {
		t.Fatalf("reference tasks = %d, want two distinct bounded tasks", len(queue.tasks))
	}
	seen := map[string]bool{}
	for _, task := range queue.tasks {
		var payload types.PaperCatalogResolvePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Scope != types.PaperCatalogScopeReference || payload.CitedDocumentID == "" {
			t.Fatalf("reference task payload = %#v", payload)
		}
		seen[payload.CitedDocumentID] = true
	}
	if !seen[first.String()] || !seen[second.String()] || seen[duplicate.String()] {
		t.Fatalf("deduplicated references = %#v", seen)
	}
}

func TestPaperInfoResolveSourceBackfillsBlankDOIWithoutOverwritingConflict(t *testing.T) {
	documentID := uuid.New()
	otherID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: documentID, Status: types.DocumentStatusProcessing},
		types.Document{ID: otherID, Status: types.DocumentStatusReady, DOI: "10.1000/owned"},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	provider := &paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
		return []types.PaperCatalogCandidate{testCatalogCandidate("crossref", "10.1000/owned", "Source Paper", 1)}, nil
	}}
	service := NewPaperInfoService(config.Config{PaperCatalogConcurrency: 1}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}}, papers, nil, nil, nil)
	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{DocumentID: documentID}); err != nil {
		t.Fatal(err)
	}
	if got := documents.documents[documentID].DOI; got != "" {
		t.Fatalf("conflicting provider DOI overwrote document: %q", got)
	}
	if len(papers.updates) != 1 {
		t.Fatalf("catalog metadata should survive DOI conflict, updates=%d", len(papers.updates))
	}
}

func TestPaperInfoResolveMetadataRejectsRevisionChangedDuringLookup(t *testing.T) {
	documentID := uuid.New()
	revision := testMarkdownSHA([]byte("# Original\n"))
	documents := readyPaperInfoDocumentRepo(documentID, revision)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID,
		Title:      "Original",
	}
	provider := &paperInfoProviderStub{
		name: "crossref",
		search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			manifest := documents.markdowns[documentID]
			manifest.SHA256 = testMarkdownSHA([]byte("# New\n"))
			documents.markdowns[documentID] = manifest
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.1000/new", "Original", 1),
			}, nil
		},
	}
	service := NewPaperInfoService(
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
		DocumentID:     documentID,
		MarkdownSHA256: revision,
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
		ID:     documentID,
		Status: types.DocumentStatusProcessing,
		DOI:    "10.1000/original",
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
	service := NewPaperInfoService(
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
		types.Document{ID: citingID, Status: types.DocumentStatusReady},
		types.Document{ID: unknownID, Status: types.DocumentStatusUnknown},
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
	service := NewPaperInfoService(
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
		DocumentID:      citingID,
		Scope:           types.PaperCatalogScopeReference,
		CitedDocumentID: unknownID,
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

func TestPaperInfoResolveUnknownSourceScopeIsUnavailable(t *testing.T) {
	unknownID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: unknownID, Status: types.DocumentStatusUnknown,
	})
	service := NewPaperInfoService(
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
	if !errors.Is(err, ErrPaperInfoCatalogUnavailable) {
		t.Fatalf("unknown source-scope resolve error = %v, want ErrPaperInfoCatalogUnavailable", err)
	}
}

func TestPaperInfoResolveUnknownReferenceRepointsCitationToStoredPaper(t *testing.T) {
	citingID := uuid.New()
	unknownID := uuid.New()
	storedID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, Status: types.DocumentStatusReady},
		types.Document{ID: unknownID, Status: types.DocumentStatusUnknown},
		types.Document{
			ID:     storedID,
			DOI:    "10.4000/stored",
			Status: types.DocumentStatusReady,
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
	service := NewPaperInfoService(
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
		DocumentID:      citingID,
		Scope:           types.PaperCatalogScopeReference,
		CitedDocumentID: unknownID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(papers.resolutions) != 1 ||
		papers.resolutions[0].CitingDocumentID != citingID ||
		papers.resolutions[0].CitedDocumentID != storedID {
		t.Fatalf("incoming citation was not repointed: %#v", papers.resolutions)
	}
	if len(papers.updates) != 0 {
		t.Fatalf("matched local paper should not update placeholder: %#v", papers.updates)
	}
}

func TestPaperInfoResolveMetadataAllowsProcessingDocumentWithoutMarkdown(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID:     documentID,
		Status: types.DocumentStatusProcessing,
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
	service := NewPaperInfoService(
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
		ID:     documentID,
		Status: types.DocumentStatusDeleting,
	})
	service := NewPaperInfoService(
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
		types.Document{ID: citingDocumentID, Status: types.DocumentStatusProcessing},
		types.Document{
			ID:     storedDocumentID,
			DOI:    "10.1000/local-doi",
			Status: types.DocumentStatusReady,
		},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[storedDocumentID.String()] = types.PaperMetadata{
		DocumentID: storedDocumentID,
		Title:      "Reliable Document Understanding: A Study",
		Authors:    []types.PaperAuthor{{Name: "Ada Lovelace"}},
	}
	service := NewPaperInfoService(
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
		types.Document{ID: citingDocumentID, Status: types.DocumentStatusProcessing},
		types.Document{ID: storedDocumentID, Status: types.DocumentStatusReady},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[storedDocumentID.String()] = types.PaperMetadata{
		DocumentID: storedDocumentID,
		Title:      "A Shared Paper Title",
		Authors:    []types.PaperAuthor{{Name: "Grace Hopper"}},
	}
	service := NewPaperInfoService(
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

func readyPaperInfoDocumentRepo(
	documentID uuid.UUID,
	revision string,
) *paperDocumentRepoStub {
	documents := newPaperDocumentRepoStub(types.Document{
		ID:     documentID,
		Status: types.DocumentStatusReady,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID,
		SHA256:     revision,
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

func testMarkdownSHA(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
