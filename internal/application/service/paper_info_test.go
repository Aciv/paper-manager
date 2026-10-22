package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

func serviceUUIDPtr(value uuid.UUID) *uuid.UUID { return &value }

// paperInfoDispatcherStub records submissions. Tests that need provider
// execution use CatalogDispatcher directly; service tests that only exercise
// dispatch/validation stay deterministic.
type paperInfoDispatcherStub struct {
	mu         sync.Mutex
	sources    []types.CatalogSourceJob
	references []types.CatalogReferenceJob
	err        error
}

func (s *paperInfoDispatcherStub) SubmitSource(_ context.Context, job types.CatalogSourceJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sources = append(s.sources, job)
	return nil
}

func (s *paperInfoDispatcherStub) SubmitReference(_ context.Context, job types.CatalogReferenceJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.references = append(s.references, job)
	return nil
}

// newPaperInfoServiceForTest keeps test setup concise while injecting a
// deterministic dispatcher stub. Provider-execution behavior is covered by
// catalog dispatcher tests.
func newPaperInfoServiceForTest(
	cfg config.Config,
	documents interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	extractor interfaces.PaperMetadataExtractor,
	catalogs interfaces.PaperCatalogRegistry,
	papers interfaces.PaperService,
	tasks interfaces.TaskEnqueuer,
	inspector interfaces.TaskInspector,
	_ any,
) interfaces.PaperInfoService {
	return NewPaperInfoService(
		cfg, documents, storage, extractor, papers, tasks, inspector,
		&paperInfoDispatcherStub{},
	)
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

type paperInfoSpanFinish struct {
	spanID uuid.UUID
	status types.ProcessingSpanStatus
	code   string
	reason string
}

type paperInfoSpanStub struct {
	mu       sync.Mutex
	starts   []paperInfoSpanStart
	finishes []paperInfoSpanFinish
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
func (s *paperInfoSpanStub) FinishSpan(
	_ context.Context, spanID uuid.UUID, status types.ProcessingSpanStatus, code, reason string,
) error {
	s.mu.Lock()
	s.finishes = append(s.finishes, paperInfoSpanFinish{
		spanID: spanID, status: status, code: code, reason: reason,
	})
	s.mu.Unlock()
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
	// Reference-specific operations are added by tests that need them; the
	// embedded interface keeps unrelated legacy tests compiling.
	interfaces.PaperService
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
	references    map[uuid.UUID]types.PaperReference
	localMatch    func(types.PaperReference, uuid.UUID) (uuid.UUID, bool, error)
}

func newPaperInfoPaperServiceStub() *paperInfoPaperServiceStub {
	return &paperInfoPaperServiceStub{
		metadata:     make(map[string]types.PaperMetadata),
		citations:    make(map[string][]types.PaperCitation),
		replacements: make(map[string][]types.PaperCitationInput),
		merges:       make(map[string][]types.PaperCitationInput),
		references:   make(map[uuid.UUID]types.PaperReference),
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
	items := append([]types.PaperCitation(nil), s.citations[documentID]...)
	for index := range items {
		if reference, ok := s.references[items[index].ReferenceID]; ok {
			cited := items[index].CitedDocumentID
			items[index].PaperReference = reference
			if cited != nil {
				items[index].CitedDocumentID = cited
			}
		}
	}
	return items, nil
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
			reference, ok := s.references[citation.ReferenceID]
			if ok && reference.CitedDocumentID != nil && *reference.CitedDocumentID == targetID {
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
	referenceID := types.PaperReferenceIDForRaw(citingID, rawReference)
	s.mu.Lock()
	defer s.mu.Unlock()
	reference := s.references[referenceID]
	reference.ReferenceID = referenceID
	reference.CitingDocumentID = citingID
	reference.RawReference = rawReference
	reference.CitedDocumentID = &resolvedID
	reference.ResolutionStatus = types.DocumentStageSucceeded
	s.references[referenceID] = reference
	citation := types.PaperCitation{
		PaperReference: reference,
		Origin:         types.PaperCitationOriginManual,
	}
	s.resolutions = append(s.resolutions, citation)
	return &citation, nil
}

func (s *paperInfoPaperServiceStub) GetReference(_ context.Context, referenceID string) (*types.PaperReference, error) {
	id, err := uuid.Parse(referenceID)
	if err != nil {
		return nil, err
	}
	reference, ok := s.references[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &reference, nil
}

func (s *paperInfoPaperServiceStub) BeginReferenceDownload(
	_ context.Context, referenceID, attemptID string,
) (*types.PaperReference, error) {
	id, err := uuid.Parse(referenceID)
	if err != nil {
		return nil, err
	}
	attempt, err := uuid.Parse(attemptID)
	if err != nil || attempt == uuid.Nil {
		return nil, errors.New("invalid download attempt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reference, ok := s.references[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	if reference.CitedDocumentID != nil {
		return &reference, ErrReferenceAlreadyResolved
	}
	failed := reference.DownloadErrorCode != nil || reference.DownloadErrorMessage != nil
	if reference.DownloadRequestedAt != nil && !failed &&
		time.Since(*reference.DownloadRequestedAt) < types.ReferenceDownloadInFlightWindow {
		return &reference, types.ErrReferenceDownloadInProgress
	}
	now := time.Now().UTC()
	reference.DownloadAttempt = &attempt
	reference.DownloadRequestedAt = &now
	reference.DownloadErrorCode = nil
	reference.DownloadErrorMessage = nil
	reference.DownloadUpdatedAt = &now
	s.references[id] = reference
	return &reference, nil
}

func (s *paperInfoPaperServiceStub) FailReferenceDownload(
	_ context.Context, referenceID, attemptID, errorCode, errorMessage string,
) error {
	id, err := uuid.Parse(referenceID)
	if err != nil {
		return err
	}
	attempt, err := uuid.Parse(attemptID)
	if err != nil || attempt == uuid.Nil {
		return errors.New("invalid download attempt")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reference, ok := s.references[id]
	if !ok || reference.CitedDocumentID != nil {
		return nil
	}
	if reference.DownloadAttempt == nil || *reference.DownloadAttempt != attempt {
		return nil
	}
	now := time.Now().UTC()
	reference.DownloadErrorCode = &errorCode
	reference.DownloadErrorMessage = &errorMessage
	reference.DownloadUpdatedAt = &now
	s.references[id] = reference
	return nil
}

func (s *paperInfoPaperServiceStub) ResolveReference(_ context.Context, referenceID, resolvedDocumentID string) (*types.PaperReference, error) {
	id, err := uuid.Parse(referenceID)
	if err != nil {
		return nil, err
	}
	documentID, err := uuid.Parse(resolvedDocumentID)
	if err != nil {
		return nil, err
	}
	reference, err := s.LinkReferenceDocument(context.Background(), id, documentID, true)
	return reference, err
}

func (s *paperInfoPaperServiceStub) LinkReferenceDocument(
	_ context.Context, referenceID, documentID uuid.UUID, overwrite bool,
) (*types.PaperReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reference := s.references[referenceID]
	if reference.CitedDocumentID != nil && !overwrite {
		return &reference, nil
	}
	if reference.ResolutionStatus.Active() {
		reference.ResolutionGeneration++
	}
	reference.ReferenceID = referenceID
	reference.CitedDocumentID = &documentID
	reference.ResolutionStatus = types.DocumentStageSucceeded
	s.references[referenceID] = reference
	return &reference, nil
}

func (s *paperInfoPaperServiceStub) FindLocalReferenceMatch(
	_ context.Context, reference types.PaperReference, excludeDocumentID uuid.UUID,
) (uuid.UUID, bool, error) {
	if s.localMatch != nil {
		return s.localMatch(reference, excludeDocumentID)
	}
	return uuid.Nil, false, nil
}

func (s *paperInfoPaperServiceStub) QueueReferenceResolution(_ context.Context, referenceID uuid.UUID) (types.PaperReference, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reference, ok := s.references[referenceID]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if reference.CitedDocumentID != nil || reference.ResolutionStatus.Active() ||
		reference.ResolutionStatus == types.DocumentStageSucceeded {
		return reference, false, nil
	}
	reference.ResolutionStatus = types.DocumentStageQueued
	reference.ResolutionGeneration++
	s.references[referenceID] = reference
	return reference, true, nil
}

func (s *paperInfoPaperServiceStub) StartReferenceResolution(_ context.Context, referenceID uuid.UUID, generation int64) (types.PaperReference, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reference, ok := s.references[referenceID]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if reference.ResolutionStatus != types.DocumentStageQueued || reference.ResolutionGeneration != generation {
		return reference, false, nil
	}
	reference.ResolutionStatus = types.DocumentStageRunning
	s.references[referenceID] = reference
	return reference, true, nil
}

func (s *paperInfoPaperServiceStub) FinishReferenceResolution(
	_ context.Context, referenceID uuid.UUID, generation int64, status types.DocumentStageStatus,
	provider, _, _ string, update *types.PaperReferenceMetadataPatch, resolvedDocumentID *uuid.UUID,
) (types.PaperReference, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reference, ok := s.references[referenceID]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if reference.ResolutionGeneration != generation || reference.ResolutionStatus != types.DocumentStageRunning {
		return reference, false, nil
	}
	applyStubPatch(&reference, update)
	reference.ResolutionStatus = status
	reference.ResolutionProvider = provider
	if resolvedDocumentID != nil {
		reference.CitedDocumentID = resolvedDocumentID
		reference.ResolutionStatus = types.DocumentStageSucceeded
	}
	s.references[referenceID] = reference
	return reference, true, nil
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
		config.Config{PaperReferenceAutoResolve: true}, documents,
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
	var source types.PaperSourceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &source); err != nil {
		t.Fatal(err)
	}
	var references types.PaperReferenceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[1].Payload(), &references); err != nil {
		t.Fatal(err)
	}
	if source.Stage != types.DocumentStageSourceLookup || source.Generation != 1 ||
		references.Stage != types.DocumentStageReferenceLookup || references.Generation != 1 {
		t.Fatalf("downstream payloads = source:%#v references:%#v", source, references)
	}
}

func TestPaperInfoExtractionRetryStartsFreshSourceLookupGeneration(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		MetadataExtractionStatus: types.DocumentStageQueued, MetadataExtractionGeneration: 2,
		SourceLookupStatus: types.DocumentStageSucceeded, SourceLookupGeneration: 1,
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/retry.md"}
	papers := newPaperInfoPaperServiceStub()
	queue := &paperInfoQueueStub{}
	service := newPaperInfoServiceForTest(
		config.Config{}, documents,
		&paperInfoStorageStub{content: map[string][]byte{"markdown/retry.md": []byte("# Corrected Paper\n")}},
		&paperInfoExtractorStub{extract: func(*types.PaperExtractionRequest) (*types.PaperExtractionResult, error) {
			return &types.PaperExtractionResult{
				Title:         &types.PaperTextCandidate{Value: "Corrected Paper", Confidence: 1},
				ExtractorName: "test",
			}, nil
		}},
		&paperInfoRegistryStub{}, papers, queue, nil, nil,
	)

	_, err := service.ExtractMetadata(context.Background(), types.PaperMetadataExtractCommand{
		DocumentID: documentID, Stage: types.DocumentStageMetadataExtraction, Generation: 2,
		RestartSourceLookup: true,
	})
	if err != nil {
		t.Fatalf("ExtractMetadata() error = %v", err)
	}
	document := documents.documents[documentID]
	if document.SourceLookupStatus != types.DocumentStageQueued || document.SourceLookupGeneration != 2 {
		t.Fatalf("source lookup stage = %s generation=%d, want queued generation=2", document.SourceLookupStatus, document.SourceLookupGeneration)
	}
	if len(queue.tasks) != 1 || queue.tasks[0].Type() != types.TypePaperSourceQueryEnqueue {
		t.Fatalf("queued tasks = %#v, want one source lookup", queue.tasks)
	}
	var source types.PaperSourceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &source); err != nil {
		t.Fatal(err)
	}
	if source.Generation != 2 {
		t.Fatalf("source lookup payload generation = %d, want 2", source.Generation)
	}
}

func TestSourceCatalogCandidateOverridesExtractionButNotStrongerSources(t *testing.T) {
	service := &paperInfoService{}
	query := types.PaperLookupQuery{
		Title:   "EfficientUICoder: A Bidirectional Token Compression Framework",
		Authors: []string{"Software", "Computing methodologies"},
	}
	candidate := types.PaperCatalogCandidate{
		Provider: "arxiv", ProviderID: "2403.12345",
		Title: query.Title, Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}, {Name: "Grace Hopper"}},
		ProviderScore: 1,
	}
	document := types.Document{ID: uuid.New()}

	for _, source := range []types.PaperMetadataSource{
		types.PaperMetadataSourceUnknown,
		types.PaperMetadataSourceExtraction,
	} {
		metadata := &types.PaperMetadata{
			Title: query.Title, Authors: []types.PaperAuthor{{Name: "Software"}, {Name: "Computing methodologies"}},
			MetadataSource: source,
		}
		if !service.sourceCandidateBetter(document, metadata, query, candidate) {
			t.Fatalf("catalog candidate should override %s metadata", source)
		}
	}

	manual := &types.PaperMetadata{
		Title: query.Title, Authors: []types.PaperAuthor{{Name: "Software"}, {Name: "Computing methodologies"}},
		MetadataSource: types.PaperMetadataSourceManual,
	}
	if service.sourceCandidateBetter(document, manual, query, candidate) {
		t.Fatal("catalog candidate must not outrank a stronger manual match")
	}
}

func TestSourceCatalogCandidateMergeableUsesTitleAndAuthors(t *testing.T) {
	document := types.Document{ID: uuid.New()}
	metadata := &types.PaperMetadata{
		Title: "ComUICoder: Component-based Reusable UI Code Generation for Complex Websites via Semantic Segmentation and Element-wise Feedback",
		Authors: []types.PaperAuthor{
			{Name: "Jingyu Xiao"},
			{Name: "Michael R. Lyu"},
		},
		MetadataSource: types.PaperMetadataSourceCatalog,
	}
	candidate := types.PaperCatalogCandidate{
		Provider: "crossref", ProviderID: "10.1145/example", DOI: "10.1145/example",
		Title: "Component-based Reusable UI Code Generation for Complex Websites via Semantic Segmentation and Fine-grained Feedback",
		Authors: []types.PaperAuthor{
			{Name: "Jingyu Xiao"},
			{Name: "Michael R. Lyu"},
		},
	}
	if !sourceCandidateMergeable(document, metadata, candidate) {
		t.Fatal("closely related catalog records should merge missing fields")
	}

	candidate.Authors = []types.PaperAuthor{{Name: "Unrelated Author"}}
	if sourceCandidateMergeable(document, metadata, candidate) {
		t.Fatal("weak title with unrelated authors must not merge")
	}

	metadata.MetadataSource = types.PaperMetadataSourceExtraction
	candidate.Title = metadata.Title
	if sourceCandidateMergeable(document, metadata, candidate) {
		t.Fatal("extraction metadata should still be replaced by a trusted catalog record")
	}
}

func TestPaperSourceQueryApplyMergesMissingCatalogFields(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		SourceLookupStatus: types.DocumentStageRunning, SourceLookupGeneration: 2,
	})
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID,
		Title:      "ComUICoder: Component-based Reusable UI Code Generation for Complex Websites via Semantic Segmentation and Element-wise Feedback",
		Abstract:   "Existing abstract",
		Authors:    []types.PaperAuthor{{Name: "Jingyu Xiao"}, {Name: "Michael R. Lyu"}},
		SourceName: "arXiv", SourceURL: "https://arxiv.org/abs/2602.19276v1",
		MetadataSource: types.PaperMetadataSourceCatalog,
	}
	service := &paperInfoService{documents: documents, papers: papers}
	candidate := types.PaperCatalogCandidate{
		Provider: "crossref", ProviderID: "10.1145/3770855.3817689",
		DOI:      "10.1145/3770855.3817689",
		Title:    "Component-based Reusable UI Code Generation for Complex Websites via Semantic Segmentation and Fine-grained Feedback",
		Abstract: "Replacement abstract",
		Authors:  []types.PaperAuthor{{Name: "Jingyu Xiao"}, {Name: "Michael R. Lyu"}},
		Venue:    "KDD '26", Pages: "5638-5649", ProviderScore: 1,
	}
	err := service.QueryApply(context.Background(), types.PaperQueryApplyCommand{
		Kind: types.CatalogJobSource, DocumentID: documentID, Generation: 2,
		Query: types.PaperLookupQuery{
			Title:   papers.metadata[documentID.String()].Title,
			Authors: []string{"Jingyu Xiao", "Michael R. Lyu"},
		},
		Candidate: &candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := documents.documents[documentID].DOI; got != "10.1145/3770855.3817689" {
		t.Fatalf("document DOI = %q", got)
	}
	if len(papers.updates) != 1 {
		t.Fatalf("metadata updates = %d, want 1", len(papers.updates))
	}
	update := papers.updates[0].update
	if update.Title != nil || update.Abstract != nil || update.Authors != nil ||
		update.SourceName != nil || update.SourceURL != nil {
		t.Fatalf("merge overwrote populated fields: %#v", update)
	}
	if update.Venue == nil || *update.Venue != "KDD '26" ||
		update.Pages == nil || *update.Pages != "5638-5649" {
		t.Fatalf("missing fields were not merged: %#v", update)
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

func TestExtractionCitationInputsPreserveBibliographyOrderAcrossSkippedRows(t *testing.T) {
	inputs := extractionCitationInputs([]string{"First reference", "  ", "Second reference"}, nil)
	if len(inputs) != 2 {
		t.Fatalf("extractionCitationInputs() = %#v, want two inputs", inputs)
	}
	if inputs[0].BibliographyOrder == nil || *inputs[0].BibliographyOrder != 0 {
		t.Fatalf("first bibliography order = %#v, want 0", inputs[0].BibliographyOrder)
	}
	if inputs[1].BibliographyOrder == nil || *inputs[1].BibliographyOrder != 2 {
		t.Fatalf("second bibliography order = %#v, want original position 2", inputs[1].BibliographyOrder)
	}
}

func TestExtractionMetadataUpdateOnlyIncludesUsableCandidates(t *testing.T) {
	result := &types.PaperExtractionResult{
		Title:    &types.PaperTextCandidate{Value: "  ", Confidence: 1},
		Abstract: &types.PaperTextCandidate{Value: "Abstract\x00 body", Confidence: 1},
		Authors: []types.PaperTextCandidate{
			{Value: "  ", Confidence: 1},
			{Value: "Ada\x00 Lovelace", Confidence: 1},
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

func TestExtractionCitationInputsRemovePostgresNULBytes(t *testing.T) {
	inputs := extractionCitationInputs(
		[]string{"[1] A\x00 reference"},
		[]types.PaperReferenceCandidate{{
			RawReference: "[1] A\x00 reference",
			Title:        "Paper\x00 title",
			Authors:      []string{"Ada\x00 Lovelace"},
		}},
	)
	if len(inputs) != 1 || inputs[0].RawReference != "[1] A reference" || inputs[0].Candidate == nil {
		t.Fatalf("citation inputs = %#v", inputs)
	}
	if inputs[0].Candidate.Title != "Paper title" || len(inputs[0].Candidate.Authors) != 1 || inputs[0].Candidate.Authors[0] != "Ada Lovelace" {
		t.Fatalf("citation candidate = %#v", inputs[0].Candidate)
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

func TestPaperInfoReferenceDispatcherQueuesBoundedReferences(t *testing.T) {
	documentID := uuid.New()
	documents := readyPaperInfoDocumentRepo(documentID)
	firstID := types.PaperReferenceIDForRaw(documentID, "Ada. 2024. One.")
	secondID := types.PaperReferenceIDForRaw(documentID, "Grace. 2023. Two.")
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	papers.references[firstID] = types.PaperReference{
		ReferenceID: firstID, CitingDocumentID: documentID, RawReference: "Ada. 2024. One.",
		ResolutionStatus: types.DocumentStageNotStarted,
	}
	papers.references[secondID] = types.PaperReference{
		ReferenceID: secondID, CitingDocumentID: documentID, RawReference: "Grace. 2023. Two.",
		ResolutionStatus: types.DocumentStageNotStarted,
	}
	papers.citations[documentID.String()] = []types.PaperCitation{
		{PaperReference: papers.references[firstID]},
		{PaperReference: papers.references[secondID]},
	}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1, PaperReferenceCatalogMaxPerDocument: 2}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{}, papers, nil, nil, nil)
	if err := service.ReferenceQueryEnqueue(context.Background(), types.PaperReferenceQueryEnqueueCommand{
		DocumentID: documentID,
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := service.(*paperInfoService).dispatcher.(*paperInfoDispatcherStub)
	dispatcher.mu.Lock()
	submitted := append([]types.CatalogReferenceJob(nil), dispatcher.references...)
	dispatcher.mu.Unlock()
	if len(submitted) != 2 {
		t.Fatalf("reference submissions = %d, want two", len(submitted))
	}
	seen := map[uuid.UUID]bool{}
	for _, job := range submitted {
		seen[job.Reference.ReferenceID] = true
		if job.Generation != 1 {
			t.Fatalf("reference generation = %d, want 1", job.Generation)
		}
	}
	if !seen[firstID] || !seen[secondID] {
		t.Fatalf("dispatched references = %#v", seen)
	}
}

func TestPaperInfoReferenceDispatcherQueuesEachPapersOwnReference(t *testing.T) {
	firstSource, secondSource := uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: firstSource, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: secondSource, SourceState: types.DocumentSourceAvailable},
	)
	papers := newPaperInfoPaperServiceStub()
	ids := []uuid.UUID{
		types.PaperReferenceIDForRaw(firstSource, "Shared target"),
		types.PaperReferenceIDForRaw(secondSource, "Shared target"),
	}
	for index, sourceID := range []uuid.UUID{firstSource, secondSource} {
		referenceID := ids[index]
		papers.references[referenceID] = types.PaperReference{
			ReferenceID: referenceID, CitingDocumentID: sourceID, RawReference: "Shared target",
			ResolutionStatus: types.DocumentStageNotStarted,
		}
		papers.metadata[sourceID.String()] = types.PaperMetadata{DocumentID: sourceID}
		papers.citations[sourceID.String()] = []types.PaperCitation{{
			PaperReference: papers.references[referenceID],
		}}
	}
	service := newPaperInfoServiceForTest(config.Config{}, documents, nil, nil,
		&paperInfoRegistryStub{}, papers, nil, nil, nil)

	for _, sourceID := range []uuid.UUID{firstSource, secondSource} {
		if err := service.ReferenceQueryEnqueue(context.Background(), types.PaperReferenceQueryEnqueueCommand{
			DocumentID: sourceID,
		}); err != nil {
			t.Fatalf("ReferenceQueryEnqueue(%s): %v", sourceID, err)
		}
	}
	dispatcher := service.(*paperInfoService).dispatcher.(*paperInfoDispatcherStub)
	dispatcher.mu.Lock()
	submitted := append([]types.CatalogReferenceJob(nil), dispatcher.references...)
	dispatcher.mu.Unlock()
	if len(submitted) != 2 {
		t.Fatalf("reference submissions = %d, want one per citing paper", len(submitted))
	}
	for _, referenceID := range ids {
		reference := papers.references[referenceID]
		if reference.ResolutionStatus != types.DocumentStageQueued || reference.ResolutionGeneration != 1 {
			t.Fatalf("reference state = %#v", reference)
		}
	}
}

func TestPaperInfoReferenceTaskCarriesOnlyTarget(t *testing.T) {
	payload := types.PaperSourceQueryEnqueuePayload{DocumentID: uuid.NewString(), Stage: types.DocumentStageSourceLookup, Generation: 1}
	command, err := payload.Command()
	if err != nil || command.DocumentID.String() != payload.DocumentID {
		t.Fatalf("single-target reference command = %#v, err=%v", command, err)
	}
}

func TestPaperInfoApplySourceKeepsNewestDocumentOnDOIConflict(t *testing.T) {
	documentID := uuid.New()
	otherID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: otherID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/owned"},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{}, papers, nil, nil, nil).(*paperInfoService)
	document, err := documents.Get(context.Background(), documentID)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := service.applySourceCatalogCandidate(context.Background(), document,
		testCatalogCandidate("crossref", "10.1000/owned", "Source Paper", 1))
	if err != nil || !applied {
		t.Fatalf("applySourceCatalogCandidate() applied=%t err=%v", applied, err)
	}
	if _, exists := documents.documents[otherID]; exists {
		t.Fatal("older DOI-owning document still exists")
	}
	if kept := documents.documents[documentID]; kept.DOI != "10.1000/owned" {
		t.Fatalf("newest document DOI = %q", kept.DOI)
	}
	if len(papers.updates) != 1 {
		t.Fatalf("newest document metadata updates = %d, want 1", len(papers.updates))
	}
}

func TestPaperInfoApplySourceDeletesOlderCurrentDocument(t *testing.T) {
	documentID := uuid.New()
	otherID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{
			ID: documentID, SourceState: types.DocumentSourceAvailable,
			CreatedAt: time.Now().UTC().Add(-time.Hour),
		},
		types.Document{
			ID: otherID, SourceState: types.DocumentSourceAvailable,
			DOI: "10.1000/owned", CreatedAt: time.Now().UTC(),
		},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{}, papers, nil, nil, nil).(*paperInfoService)
	document, err := documents.Get(context.Background(), documentID)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := service.applySourceCatalogCandidate(context.Background(), document,
		testCatalogCandidate("crossref", "10.1000/owned", "Source Paper", 1))
	if err != nil || applied {
		t.Fatalf("applySourceCatalogCandidate() applied=%t err=%v, want false", applied, err)
	}
	if _, exists := documents.documents[documentID]; exists {
		t.Fatal("older current document still exists")
	}
	if kept := documents.documents[otherID]; kept.DOI != "10.1000/owned" {
		t.Fatalf("newest document = %#v", kept)
	}
	if len(papers.updates) != 0 {
		t.Fatalf("deleted older document received metadata updates=%d", len(papers.updates))
	}
}

func TestNewestPaperDocumentPrefersStoredSourceOverCreationTime(t *testing.T) {
	now := time.Now().UTC()
	sourcedID, unsourcedID := uuid.New(), uuid.New()
	sourced := types.Document{
		ID: sourcedID, SourceState: types.DocumentSourceAvailable,
		StorageKey: "paper/sourced.pdf", CreatedAt: now.Add(-time.Hour),
	}
	unsourced := types.Document{
		ID: unsourcedID, SourceState: types.DocumentSourceAvailable,
		CreatedAt: now,
	}
	newest, older := newestPaperDocument(sourced, unsourced)
	if newest.ID != sourcedID || older.ID != unsourcedID {
		t.Fatalf("source preference = newest:%s older:%s", newest.ID, older.ID)
	}

	olderSourced := sourced
	olderSourced.ID = uuid.New()
	newerSourced := types.Document{
		ID: uuid.New(), SourceState: types.DocumentSourceAvailable,
		StorageKey: "paper/newer.pdf", CreatedAt: now,
	}
	newest, older = newestPaperDocument(olderSourced, newerSourced)
	if newest.ID != newerSourced.ID || older.ID != olderSourced.ID {
		t.Fatalf("time preference = newest:%s older:%s", newest.ID, older.ID)
	}
}

func TestPaperInfoApplySourceRetriesDuplicateReplacementRace(t *testing.T) {
	documentID := uuid.New()
	otherID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: otherID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/owned"},
	)
	documents.replaceFailures = 1
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, Title: "Source Paper"}
	service := newPaperInfoServiceForTest(config.Config{PaperCatalogConcurrency: 1}, documents, &paperInfoStorageStub{}, &paperInfoExtractorStub{}, &paperInfoRegistryStub{}, papers, nil, nil, nil).(*paperInfoService)
	document, err := documents.Get(context.Background(), documentID)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := service.applySourceCatalogCandidate(context.Background(), document,
		testCatalogCandidate("crossref", "10.1000/owned", "Source Paper", 1))
	if err != nil || !applied {
		t.Fatalf("applySourceCatalogCandidate() applied=%t err=%v", applied, err)
	}
	if _, exists := documents.documents[otherID]; exists {
		t.Fatal("older DOI-owning document still exists after retry")
	}
	if kept := documents.documents[documentID]; kept.DOI != "10.1000/owned" {
		t.Fatalf("newest document DOI = %q after retry", kept.DOI)
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

func TestPaperInfoSourceQueryEnqueueSubmitsWithoutMarkdown(t *testing.T) {
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

	err := service.PaperSourceQueryEnqueue(context.Background(), types.PaperSourceQueryEnqueueCommand{
		DocumentID: documentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := service.(*paperInfoService).dispatcher.(*paperInfoDispatcherStub)
	dispatcher.mu.Lock()
	submitted := append([]types.CatalogSourceJob(nil), dispatcher.sources...)
	dispatcher.mu.Unlock()
	if len(submitted) != 1 {
		t.Fatalf("source submissions = %d, want 1", len(submitted))
	}
	if submitted[0].DocumentID != documentID || submitted[0].Query.Title != "Concurrent Paper Lookup" {
		t.Fatalf("source job = %#v", submitted[0])
	}
}

func TestPaperInfoSourceQueryEnqueueRejectsDeletingDocument(t *testing.T) {
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

	err := service.PaperSourceQueryEnqueue(context.Background(), types.PaperSourceQueryEnqueueCommand{
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

func TestScorePaperCatalogCandidateRejectsConflictingDOIExactArxiv(t *testing.T) {
	query := types.PaperLookupQuery{
		DOI:       "10.48550/arxiv.1810.04805",
		SourceURL: "https://arxiv.org/pdf/1810.04805",
		Title:     "BERT: Pre-training of Deep Bidirectional Transformers for Language Understanding",
	}
	// This mirrors an OpenAlex merged record: its landing page is arXiv
	// 1810.04805, but its DOI, title and authors belong to another work.
	conflicting := types.PaperCatalogCandidate{
		DOI:       "10.4230/lipics.cosit.2022.18",
		SourceURL: "http://arxiv.org/abs/1810.04805",
		Title:     "AI-Assisted Pipeline for Dynamic Generation of Trustworthy Health Supplement Content at Scale",
		Authors:   []types.PaperAuthor{{Name: "Kefallinos, Dionysios"}},
		Provider:  "openalex", ProviderID: "W2896457183", ProviderScore: 1,
	}
	score, exact := scorePaperCatalogCandidate(query, conflicting)
	if exact {
		t.Fatal("conflicting candidate DOI must not produce an exact arXiv match")
	}
	if score >= minPaperCatalogMatchScore {
		t.Fatalf("conflicting candidate score = %f, want below threshold %f", score, minPaperCatalogMatchScore)
	}

	correct := types.PaperCatalogCandidate{
		DOI:       "10.48550/arxiv.1810.04805",
		SourceURL: "https://arxiv.org/abs/1810.04805",
		Title:     query.Title,
		Authors:   []types.PaperAuthor{{Name: "Devlin, Jacob"}},
		Provider:  "arxiv", ProviderID: "1810.04805", ProviderScore: 1,
	}
	correctScore, correctExact := scorePaperCatalogCandidate(query, correct)
	if !correctExact || correctScore != 1 {
		t.Fatalf("correct arXiv candidate = score %f exact %v, want 1 true", correctScore, correctExact)
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
	if titleScore < minPaperCatalogMatchScore {
		t.Fatalf("strong title with a wrong extracted author should still match: %f", titleScore)
	}
}

func TestScorePaperCatalogCandidateRequiresAuthorsOnlyForWeakTitle(t *testing.T) {
	query := types.PaperLookupQuery{
		Title:   "Reliable Visual Document Understanding System",
		Authors: []string{"Ada Lovelace"},
	}
	weakTitle := "Reliable Visual Document Understanding Toolkit"
	withMatchingAuthor := types.PaperCatalogCandidate{
		Title: weakTitle, Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}},
		ProviderScore: 1,
	}
	withWrongAuthor := withMatchingAuthor
	withWrongAuthor.Authors = []types.PaperAuthor{{Name: "Grace Hopper"}}

	matchingScore, _ := scorePaperCatalogCandidate(query, withMatchingAuthor)
	wrongAuthorScore, _ := scorePaperCatalogCandidate(query, withWrongAuthor)
	if matchingScore < minPaperCatalogMatchScore {
		t.Fatalf("weak title with matching author score = %f, want at least %f",
			matchingScore, minPaperCatalogMatchScore)
	}
	if wrongAuthorScore != 0 {
		t.Fatalf("weak title with wrong author score = %f, want 0", wrongAuthorScore)
	}
}

func TestPaperSourceQueryApplyPersistsNoMatchOutcome(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		SourceLookupStatus: types.DocumentStageRunning, SourceLookupGeneration: 2,
	})
	service := &paperInfoService{documents: documents}

	err := service.QueryApply(context.Background(), types.PaperQueryApplyCommand{
		Kind: types.CatalogJobSource, DocumentID: documentID, Generation: 2,
		Final: true, Outcome: types.CatalogOutcomeNoMatch,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := documents.GetStage(context.Background(), documentID, types.DocumentStageSourceLookup)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != types.DocumentStageSucceeded || state.ErrorCode == nil ||
		*state.ErrorCode != types.ProcessingErrorNoMatch {
		t.Fatalf("source lookup state = %#v, want succeeded NO_MATCH", state)
	}
}

func readyPaperInfoDocumentRepo(id uuid.UUID) *paperDocumentRepoStub {
	return newPaperDocumentRepoStub(types.Document{
		ID: id, SourceState: types.DocumentSourceAvailable,
		MarkdownStatus: types.DocumentStageSucceeded,
	})
}

func testCatalogCandidate(provider, doi, title string, providerScore float64) types.PaperCatalogCandidate {
	providerID := doi
	if providerID == "" {
		providerID = provider + ":" + title
	}
	return types.PaperCatalogCandidate{
		Provider:      provider,
		ProviderID:    providerID,
		DOI:           doi,
		Title:         title,
		Authors:       []types.PaperAuthor{{Name: "Ada Lovelace"}},
		Tags:          []string{},
		ProviderScore: providerScore,
		RetrievedAt:   time.Now().UTC(),
	}
}
