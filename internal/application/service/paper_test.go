package service

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type paperDocumentRepoStub struct {
	documents       map[uuid.UUID]types.Document
	markdowns       map[uuid.UUID]types.DocumentMarkdown
	blocks          map[uuid.UUID][]types.Block
	search          func(types.DocumentSearch) (types.DocumentPage, error)
	replaceFailures int
}

func (*paperDocumentRepoStub) CitationTargetProgress(context.Context, uuid.UUID) (types.PaperReferenceProgress, error) {
	return types.PaperReferenceProgress{}, nil
}

func (r *paperDocumentRepoStub) GetStage(_ context.Context, id uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, error) {
	document, ok := r.documents[id]
	if !ok {
		return types.DocumentStageState{}, sql.ErrNoRows
	}
	state, err := document.StageState(stage)
	if err == nil && state.Status == "" {
		state.Status = types.DocumentStageNotStarted
	}
	return state, err
}

func (r *paperDocumentRepoStub) QueueStageNewGeneration(_ context.Context, id uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, bool, error) {
	document, ok := r.documents[id]
	if !ok {
		return types.DocumentStageState{}, false, sql.ErrNoRows
	}
	state, applied, err := queueStubDocumentStage(&document, stage)
	r.documents[id] = document
	return state, applied, err
}

func (r *paperDocumentRepoStub) TryStartStage(_ context.Context, id uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error) {
	return r.transitionStage(id, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued}, types.DocumentStageRunning)
}

func (r *paperDocumentRepoStub) MarkStageRetryQueued(_ context.Context, id uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error) {
	return r.transitionStage(id, stage, generation, []types.DocumentStageStatus{types.DocumentStageRunning}, types.DocumentStageQueued)
}

func (r *paperDocumentRepoStub) FinishStage(_ context.Context, id uuid.UUID, stage types.DocumentStage, generation int64, status types.DocumentStageStatus, _, _ string) (types.DocumentStageState, bool, error) {
	return r.transitionStage(id, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning}, status)
}

func (r *paperDocumentRepoStub) CancelStage(_ context.Context, id uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error) {
	return r.transitionStage(id, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning}, types.DocumentStageCancelled)
}

func (r *paperDocumentRepoStub) AllowsStageWork(_ context.Context, id uuid.UUID, stage types.DocumentStage, generation int64) (bool, error) {
	state, err := r.GetStage(context.Background(), id, stage)
	return err == nil && state.Generation == generation && state.Status == types.DocumentStageRunning, err
}

func (r *paperDocumentRepoStub) transitionStage(id uuid.UUID, stage types.DocumentStage, generation int64, from []types.DocumentStageStatus, to types.DocumentStageStatus) (types.DocumentStageState, bool, error) {
	document, ok := r.documents[id]
	if !ok {
		return types.DocumentStageState{}, false, sql.ErrNoRows
	}
	state, applied, err := transitionStubDocumentStage(&document, stage, generation, from, to)
	r.documents[id] = document
	return state, applied, err
}

func newPaperDocumentRepoStub(documents ...types.Document) *paperDocumentRepoStub {
	repo := &paperDocumentRepoStub{
		documents: make(map[uuid.UUID]types.Document),
		markdowns: make(map[uuid.UUID]types.DocumentMarkdown),
		blocks:    make(map[uuid.UUID][]types.Block),
	}
	for _, document := range documents {
		document = normalizeTestDocumentSource(document)
		repo.documents[document.ID] = document
	}
	return repo
}

func (r *paperDocumentRepoStub) Create(
	_ context.Context, document types.Document,
) (types.Document, error) {
	document = normalizeTestDocumentSource(document)
	r.documents[document.ID] = document
	return document, nil
}

func (r *paperDocumentRepoStub) Get(
	_ context.Context, id uuid.UUID,
) (types.Document, error) {
	document, ok := r.documents[id]
	if !ok {
		return types.Document{}, sql.ErrNoRows
	}
	document = normalizeTestDocumentSource(document)
	r.documents[id] = document
	return document, nil
}

func normalizeTestDocumentSource(document types.Document) types.Document {
	if document.SourceState == "" {
		document.SourceState = types.DocumentSourceAvailable
	}
	if document.SourceState == types.DocumentSourceAvailable && document.MarkdownStatus == "" {
		document.MarkdownStatus = types.DocumentStageSucceeded
	}
	return document
}

func (r *paperDocumentRepoStub) ReplaceOlderDocument(
	_ context.Context, olderID, newestID uuid.UUID,
) (types.DocumentFileKeys, error) {
	if r.replaceFailures > 0 {
		r.replaceFailures--
		return types.DocumentFileKeys{}, sql.ErrNoRows
	}
	older, ok := r.documents[olderID]
	if !ok {
		return types.DocumentFileKeys{}, sql.ErrNoRows
	}
	newest, ok := r.documents[newestID]
	if !ok {
		return types.DocumentFileKeys{}, sql.ErrNoRows
	}
	if newest.DOI == "" {
		newest.DOI = older.DOI
	}
	r.documents[newestID] = newest
	delete(r.documents, olderID)
	return types.DocumentFileKeys{StorageKeys: []string{older.StorageKey}}, nil
}

func (r *paperDocumentRepoStub) Search(
	_ context.Context, search types.DocumentSearch,
) (types.DocumentPage, error) {
	if r.search != nil {
		return r.search(search)
	}
	page := types.DocumentPage{Page: search.Page, PageSize: search.PageSize}
	for id, stored := range r.documents {
		document := normalizeTestDocumentSource(stored)
		r.documents[id] = document
		if search.DOI != "" && !strings.EqualFold(document.DOI, search.DOI) {
			continue
		}
		if search.FileSHA256 != "" && !strings.EqualFold(document.FileSHA256, search.FileSHA256) {
			continue
		}
		if search.SourceState != "" && document.SourceState != search.SourceState {
			continue
		}
		excluded := false
		for _, excludeID := range search.ExcludeIDs {
			if document.ID == excludeID {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		page.Items = append(page.Items, document)
	}
	page.Total = int64(len(page.Items))
	return page, nil
}

func (r *paperDocumentRepoStub) GetMarkdown(
	_ context.Context, documentID uuid.UUID,
) (types.DocumentMarkdown, error) {
	if markdown, ok := r.markdowns[documentID]; ok {
		return markdown, nil
	}
	return types.DocumentMarkdown{}, sql.ErrNoRows
}

func (*paperDocumentRepoStub) ReplaceParsedOutput(
	context.Context, types.DocumentMarkdown, []types.Block, int64,
) error {
	return nil
}

func (r *paperDocumentRepoStub) ListBlocks(
	_ context.Context, documentID uuid.UUID, pageNumber int,
) ([]types.Block, error) {
	blocks := r.blocks[documentID]
	if pageNumber == 0 {
		return append([]types.Block(nil), blocks...), nil
	}
	filtered := make([]types.Block, 0)
	for _, block := range blocks {
		if block.Location != nil &&
			block.Location.PageNumber != nil &&
			*block.Location.PageNumber == pageNumber {
			filtered = append(filtered, block)
		}
	}
	return filtered, nil
}

func (*paperDocumentRepoStub) GetBlock(
	context.Context, uuid.UUID, uuid.UUID,
) (types.Block, error) {
	return types.Block{}, sql.ErrNoRows
}

func (r *paperDocumentRepoStub) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := r.documents[id]; !ok {
		return sql.ErrNoRows
	}
	delete(r.documents, id)
	return nil
}

func (r *paperDocumentRepoStub) MarkDeleting(_ context.Context, id uuid.UUID) (types.DocumentSourceState, bool, error) {
	document, ok := r.documents[id]
	if !ok {
		return "", false, sql.ErrNoRows
	}
	previous := document.SourceState
	if previous == types.DocumentSourceDeleting || previous == types.DocumentSourceMerged {
		return previous, false, nil
	}
	document.SourceState = types.DocumentSourceDeleting
	r.documents[id] = document
	return previous, true, nil
}

func (r *paperDocumentRepoStub) RestoreSourceState(_ context.Context, id uuid.UUID, state types.DocumentSourceState) error {
	document, ok := r.documents[id]
	if ok && document.SourceState == types.DocumentSourceDeleting {
		document.SourceState = state
		r.documents[id] = document
	}
	return nil
}

func (r *paperDocumentRepoStub) UpdateDocumentInfo(
	_ context.Context,
	id uuid.UUID,
	originalFilename *string,
	doi *string,
) (types.Document, error) {
	document, ok := r.documents[id]
	if !ok {
		return types.Document{}, sql.ErrNoRows
	}
	if originalFilename != nil {
		document.OriginalFilename = *originalFilename
	}
	if doi != nil {
		document.DOI = *doi
	}
	r.documents[id] = document
	return document, nil
}

func (r *paperDocumentRepoStub) SetDOIIfEmpty(
	_ context.Context, id uuid.UUID, doi string,
) (bool, error) {
	document, ok := r.documents[id]
	if !ok {
		return false, sql.ErrNoRows
	}
	if document.DOI != "" {
		return false, nil
	}
	for otherID, other := range r.documents {
		if otherID != id && other.DOI == doi && doi != "" {
			return false, types.ErrDuplicateDOI
		}
	}
	document.DOI = doi
	r.documents[id] = document
	return true, nil
}

type paperMetadataRepoStub struct {
	items      map[uuid.UUID]types.PaperMetadata
	lastSearch types.PaperSearch
}

func newPaperMetadataRepoStub() *paperMetadataRepoStub {
	return &paperMetadataRepoStub{items: make(map[uuid.UUID]types.PaperMetadata)}
}

func (r *paperMetadataRepoStub) GetByDocumentID(
	_ context.Context, documentID uuid.UUID,
) (types.PaperMetadata, error) {
	metadata, ok := r.items[documentID]
	if !ok {
		return types.PaperMetadata{}, sql.ErrNoRows
	}
	return metadata, nil
}

func (r *paperMetadataRepoStub) Upsert(
	_ context.Context,
	documentID uuid.UUID,
	update types.PaperMetadataUpdate,
) (types.PaperMetadata, error) {
	metadata := r.items[documentID]
	metadata.DocumentID = documentID
	if update.Title != nil {
		metadata.Title = *update.Title
	}
	if update.Authors != nil {
		metadata.Authors = append([]types.PaperAuthor(nil), (*update.Authors)...)
	}
	if update.Tags != nil {
		metadata.Tags = append([]string(nil), (*update.Tags)...)
	}
	if update.Venue != nil {
		metadata.Venue = *update.Venue
	}
	if update.SourceURL != nil {
		metadata.SourceURL = *update.SourceURL
	}
	if update.PublishedAt != nil {
		metadata.PublishedAt = update.PublishedAt
	}
	if update.Volume != nil {
		metadata.Volume = *update.Volume
	}
	if update.Pages != nil {
		metadata.Pages = *update.Pages
	}
	if update.MetadataSource != nil {
		metadata.MetadataSource = *update.MetadataSource
	}
	r.items[documentID] = metadata
	return metadata, nil
}

func (r *paperMetadataRepoStub) Search(
	_ context.Context, search types.PaperSearch,
) (types.PaperMetadataPage, error) {
	r.lastSearch = search
	return types.PaperMetadataPage{
		Items:    []types.PaperMetadata{},
		Page:     search.Page,
		PageSize: search.PageSize,
	}, nil
}

type paperCitationRepoStub struct {
	items map[uuid.UUID]types.PaperCitation
}

func newPaperCitationRepoStub() *paperCitationRepoStub {
	return &paperCitationRepoStub{items: make(map[uuid.UUID]types.PaperCitation)}
}

func normalizeStubCitation(citation *types.PaperCitation) {
	if citation.CitingDocumentID == uuid.Nil {
		return
	}
	if citation.ReferenceID == uuid.Nil {
		citation.ReferenceID = types.PaperReferenceIDForRaw(citation.CitingDocumentID, citation.RawReference)
	}
	citation.ReferenceKey = types.PaperCitationReferenceKey(citation.RawReference)
	if citation.ResolutionStatus == "" {
		citation.ResolutionStatus = types.DocumentStageNotStarted
	}
	if citation.MetadataSource == "" {
		citation.MetadataSource = types.PaperMetadataSourceUnknown
	}
	if citation.Authors == nil {
		citation.Authors = []types.PaperAuthor{}
	}
}

func (r *paperCitationRepoStub) findByKey(citingDocumentID uuid.UUID, key string) (types.PaperCitation, bool) {
	for _, citation := range r.items {
		if citation.CitingDocumentID == citingDocumentID && citation.ReferenceKey == key {
			return citation, true
		}
	}
	return types.PaperCitation{}, false
}

func (r *paperCitationRepoStub) AddCitation(
	_ context.Context, citation types.PaperCitation,
) (types.PaperCitation, error) {
	normalizeStubCitation(&citation)
	if err := citation.Validate(); err != nil {
		return types.PaperCitation{}, err
	}
	if existing, ok := r.findByKey(citation.CitingDocumentID, citation.ReferenceKey); ok {
		mergeStubCitation(&existing, citation)
		r.items[existing.ReferenceID] = existing
		return existing, nil
	}
	r.items[citation.ReferenceID] = citation
	return citation, nil
}

func mergeStubCitation(existing *types.PaperCitation, incoming types.PaperCitation) {
	if incoming.RawReference != "" {
		existing.RawReference = incoming.RawReference
	}
	if incoming.Title != "" {
		existing.Title = incoming.Title
	}
	if incoming.DOI != "" {
		existing.DOI = incoming.DOI
	}
	if len(incoming.Authors) > 0 {
		existing.Authors = append([]types.PaperAuthor(nil), incoming.Authors...)
	}
	if incoming.Venue != "" {
		existing.Venue = incoming.Venue
	}
	if incoming.PublishedAt != nil {
		existing.PublishedAt = incoming.PublishedAt
	}
	if incoming.SourceURL != "" {
		existing.SourceURL = incoming.SourceURL
	}
	if incoming.MetadataSource != "" {
		existing.MetadataSource = incoming.MetadataSource
	}
	existing.Origin = incoming.Origin
	if incoming.CitedDocumentID != nil {
		existing.CitedDocumentID = incoming.CitedDocumentID
		existing.ResolutionStatus = types.DocumentStageSucceeded
	}
}

func (r *paperCitationRepoStub) ReplaceCitations(
	_ context.Context,
	citingDocumentID uuid.UUID,
	citations []types.PaperCitation,
) error {
	for id, citation := range r.items {
		if citation.CitingDocumentID == citingDocumentID {
			delete(r.items, id)
		}
	}
	for _, citation := range citations {
		normalizeStubCitation(&citation)
		citation.CitingDocumentID = citingDocumentID
		if err := citation.Validate(); err != nil {
			return err
		}
		r.items[citation.ReferenceID] = citation
	}
	return nil
}

func (r *paperCitationRepoStub) MergeAutomaticCitations(
	_ context.Context,
	citingDocumentID uuid.UUID,
	citations []types.PaperCitation,
) ([]types.PaperCitation, error) {
	manual := make(map[string]types.PaperCitation)
	for id, citation := range r.items {
		if citation.CitingDocumentID != citingDocumentID {
			continue
		}
		if citation.EffectiveOrigin() == types.PaperCitationOriginExtraction {
			delete(r.items, id)
			continue
		}
		manual[citation.ReferenceKey] = citation
	}
	for _, citation := range citations {
		normalizeStubCitation(&citation)
		citation.CitingDocumentID = citingDocumentID
		citation.Origin = types.PaperCitationOriginExtraction
		if _, exists := manual[citation.ReferenceKey]; exists {
			continue
		}
		if err := citation.Validate(); err != nil {
			return nil, err
		}
		r.items[citation.ReferenceID] = citation
	}
	return r.ListByCitingDocumentID(context.Background(), citingDocumentID)
}

func (r *paperCitationRepoStub) RemoveCitation(
	_ context.Context, citingDocumentID, referenceID uuid.UUID,
) error {
	citation, ok := r.items[referenceID]
	if !ok || citation.CitingDocumentID != citingDocumentID {
		return sql.ErrNoRows
	}
	delete(r.items, referenceID)
	return nil
}

func (r *paperCitationRepoStub) ListByCitingDocumentID(
	_ context.Context, citingDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	items := make([]types.PaperCitation, 0)
	for _, citation := range r.items {
		if citation.CitingDocumentID == citingDocumentID {
			items = append(items, citation)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ReferenceID.String() < items[j].ReferenceID.String()
	})
	return items, nil
}
func (r *paperCitationRepoStub) ListByCitingDocumentIDPage(
	ctx context.Context, id uuid.UUID, offset, limit int,
) (types.PaperCitationPage, error) {
	items, err := r.ListByCitingDocumentID(ctx, id)
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

func (r *paperCitationRepoStub) ListByCitedDocumentID(
	_ context.Context, citedDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	items := make([]types.PaperCitation, 0)
	for _, citation := range r.items {
		if citation.CitedDocumentID != nil && *citation.CitedDocumentID == citedDocumentID {
			items = append(items, citation)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CitingDocumentID.String() < items[j].CitingDocumentID.String()
	})
	return items, nil
}

func (r *paperCitationRepoStub) GetReference(_ context.Context, id uuid.UUID) (types.PaperReference, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, sql.ErrNoRows
	}
	return citation.PaperReference, nil
}

func (r *paperCitationRepoStub) QueueResolution(_ context.Context, id uuid.UUID) (types.PaperReference, bool, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if citation.CitedDocumentID != nil || citation.ResolutionStatus.Active() ||
		citation.ResolutionStatus == types.DocumentStageSucceeded {
		return citation.PaperReference, false, nil
	}
	citation.ResolutionStatus = types.DocumentStageQueued
	citation.ResolutionGeneration++
	r.items[id] = citation
	return citation.PaperReference, true, nil
}

func (r *paperCitationRepoStub) StartResolution(_ context.Context, id uuid.UUID, generation int64) (types.PaperReference, bool, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if citation.ResolutionStatus != types.DocumentStageQueued || citation.ResolutionGeneration != generation {
		return citation.PaperReference, false, nil
	}
	citation.ResolutionStatus = types.DocumentStageRunning
	r.items[id] = citation
	return citation.PaperReference, true, nil
}

func (r *paperCitationRepoStub) FinishResolution(
	_ context.Context, id uuid.UUID, generation int64, status types.DocumentStageStatus,
	provider, _, _ string, update *types.PaperReferenceMetadataPatch, resolvedDocumentID *uuid.UUID,
) (types.PaperReference, bool, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if citation.ResolutionGeneration != generation || citation.ResolutionStatus != types.DocumentStageRunning {
		return citation.PaperReference, false, nil
	}
	applyStubPatch(&citation.PaperReference, update)
	if resolvedDocumentID != nil {
		citation.CitedDocumentID = resolvedDocumentID
		citation.ResolutionStatus = types.DocumentStageSucceeded
	} else {
		citation.ResolutionStatus = status
	}
	citation.ResolutionProvider = provider
	r.items[id] = citation
	return citation.PaperReference, true, nil
}

func (r *paperCitationRepoStub) LinkResolvedDocument(
	_ context.Context, id uuid.UUID, documentID uuid.UUID, overwrite bool,
) (types.PaperReference, bool, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if citation.CitingDocumentID == documentID {
		return citation.PaperReference, false, types.ErrSelfPaperCitation
	}
	if citation.CitedDocumentID != nil {
		if *citation.CitedDocumentID == documentID {
			return citation.PaperReference, false, nil
		}
		if !overwrite {
			return citation.PaperReference, false, nil
		}
	}
	if citation.ResolutionStatus.Active() {
		citation.ResolutionGeneration++
	}
	citation.CitedDocumentID = &documentID
	citation.ResolutionStatus = types.DocumentStageSucceeded
	r.items[id] = citation
	return citation.PaperReference, true, nil
}

func applyStubPatch(reference *types.PaperReference, patch *types.PaperReferenceMetadataPatch) {
	if patch == nil {
		return
	}
	if patch.DOI != nil {
		reference.DOI = *patch.DOI
	}
	if patch.Title != nil {
		reference.Title = *patch.Title
	}
	if patch.Authors != nil {
		reference.Authors = append([]types.PaperAuthor(nil), (*patch.Authors)...)
	}
	if patch.Venue != nil {
		reference.Venue = *patch.Venue
	}
	if patch.PublishedAt != nil {
		reference.PublishedAt = patch.PublishedAt
	}
	if patch.SourceURL != nil {
		reference.SourceURL = *patch.SourceURL
	}
	if patch.MetadataSource != nil {
		reference.MetadataSource = *patch.MetadataSource
	}
}

func newPaperServiceForTest(
	documents ...types.Document,
) (*paperService, *paperDocumentRepoStub, *paperMetadataRepoStub, *paperCitationRepoStub) {
	documentRepo := newPaperDocumentRepoStub(documents...)
	metadataRepo := newPaperMetadataRepoStub()
	citationRepo := newPaperCitationRepoStub()
	service := NewPaperService(documentRepo, metadataRepo, citationRepo).(*paperService)
	return service, documentRepo, metadataRepo, citationRepo
}

func TestPaperServiceRejectsMissingCitationEndpoint(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	missingID := uuid.New()

	_, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		CitedDocumentID: &missingID,
		RawReference:    "Missing Author. Missing Paper.",
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("AddCitation() error = %v, want sql.ErrNoRows", err)
	}
	if len(citations.items) != 0 {
		t.Fatal("citation was written with a missing target")
	}
}

func TestPaperServiceRejectsSelfCitation(t *testing.T) {
	document := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, _ := newPaperServiceForTest(document)

	_, err := service.AddCitation(context.Background(), document.ID.String(), types.PaperCitationInput{
		CitedDocumentID: &document.ID,
		RawReference:    "Self reference.",
	})
	if !errors.Is(err, types.ErrSelfPaperCitation) {
		t.Fatalf("AddCitation() error = %v, want ErrSelfPaperCitation", err)
	}
}

func TestPaperServiceReusesReferenceAndPreservesRawReference(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	rawReference := "  Example Author. 2026. Example Paper.  "

	first, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: rawReference,
	})
	if err != nil {
		t.Fatalf("first AddCitation() error = %v", err)
	}
	second, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: rawReference,
	})
	if err != nil {
		t.Fatalf("second AddCitation() error = %v", err)
	}
	if first.ReferenceID != second.ReferenceID {
		t.Fatalf("reference IDs differ: %s != %s", first.ReferenceID, second.ReferenceID)
	}
	if len(citations.items) != 1 {
		t.Fatalf("citation count = %d, want 1", len(citations.items))
	}
	if first.RawReference != rawReference {
		t.Fatalf("raw reference = %q, want original %q", first.RawReference, rawReference)
	}
	if first.CitedDocumentID != nil {
		t.Fatal("unresolved reference unexpectedly has a cited document")
	}
}

func TestPaperServiceStoresStructuredCandidateOnReference(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	raw := "Ada Lovelace. 2024. Structured Citation. Example Venue."
	_, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: raw,
		Candidate: &types.PaperReferenceCandidate{
			RawReference: raw, Authors: []string{"Ada Lovelace"}, Title: "Structured Citation",
			Venue: "Example Venue", PublishedYear: 2024, DOI: "10.1000/example", ArXivID: " arXiv 2401.01234v2 ", Volume: "2", Pages: "10-20", Confidence: 0.9,
		},
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	stored := citations.items[types.PaperReferenceIDForRaw(citing.ID, raw)]
	if stored.Title != "Structured Citation" || stored.Venue != "Example Venue" ||
		len(stored.Authors) != 1 || stored.PublishedAt == nil || stored.DOI != "10.1000/example" {
		t.Fatalf("structured reference metadata not stored: %#v", stored)
	}
	if stored.SourceURL != "https://arxiv.org/abs/2401.01234" {
		t.Fatalf("reference arXiv source URL = %q", stored.SourceURL)
	}
}

func TestPaperServiceResolveCitationLinksSharedReference(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	resolved := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, documents, _, citations := newPaperServiceForTest(citing, resolved)
	rawReference := "Example Author. 2026. Example Paper."

	unresolved, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: rawReference,
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	resolvedCitation, err := service.ResolveCitation(
		context.Background(),
		citing.ID.String(),
		resolved.ID.String(),
		rawReference,
	)
	if err != nil {
		t.Fatalf("ResolveCitation() error = %v", err)
	}
	if resolvedCitation.CitedDocumentID == nil || *resolvedCitation.CitedDocumentID != resolved.ID {
		t.Fatalf("resolved target = %v, want %s", resolvedCitation.CitedDocumentID, resolved.ID)
	}
	if _, ok := documents.documents[unresolved.ReferenceID]; ok {
		t.Fatal("reference ID unexpectedly created a document row")
	}
	if citations.items[unresolved.ReferenceID].CitedDocumentID == nil {
		t.Fatal("reference row was not linked")
	}

	forward, err := service.ListCitations(context.Background(), citing.ID.String())
	if err != nil {
		t.Fatalf("ListCitations() error = %v", err)
	}
	reverse, err := service.ListCitedBy(context.Background(), resolved.ID.String())
	if err != nil {
		t.Fatalf("ListCitedBy() error = %v", err)
	}
	if len(forward) != 1 || forward[0].CitedDocumentID == nil || *forward[0].CitedDocumentID != resolved.ID {
		t.Fatalf("forward citations = %#v", forward)
	}
	if len(reverse) != 1 || reverse[0].CitingDocumentID != citing.ID {
		t.Fatalf("reverse citations = %#v", reverse)
	}
}

func TestPaperServiceMergeExtractedCitationsRetainsResolvedReference(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	resolved := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing, resolved)
	raw := "[7] Ada Lovelace. Notes on the analytical engine."

	first, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
	}})
	if err != nil || len(first) != 1 {
		t.Fatalf("first MergeExtractedCitations() items=%#v err=%v", first, err)
	}
	referenceID := first[0].ReferenceID
	if citations.items[referenceID].CitedDocumentID != nil {
		t.Fatal("new reference unexpectedly resolved")
	}
	if _, err := service.ResolveCitation(context.Background(), citing.ID.String(), resolved.ID.String(), raw); err != nil {
		t.Fatalf("ResolveCitation() error = %v", err)
	}

	second, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), []types.PaperCitationInput{{
		RawReference: "  [7] ADA Lovelace. Notes on the analytical engine. ",
	}})
	if err != nil {
		t.Fatalf("second MergeExtractedCitations() error = %v", err)
	}
	if len(second) != 1 || second[0].ReferenceID != referenceID ||
		second[0].CitedDocumentID == nil || *second[0].CitedDocumentID != resolved.ID {
		t.Fatalf("second merge = %#v, want resolved reference %s", second, referenceID)
	}
	if second[0].EffectiveOrigin() != types.PaperCitationOriginExtraction {
		t.Fatalf("merged citation origin = %q, want extraction", second[0].Origin)
	}
}

func TestPaperServiceMergeExtractedCitationsLinksLocalDOI(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	target := types.Document{
		ID: uuid.New(), SourceState: types.DocumentSourceAvailable, DOI: "10.1000/local",
	}
	service, _, _, _ := newPaperServiceForTest(citing, target)
	raw := "Ada Lovelace. 2024. A local paper."
	items, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
		Candidate: &types.PaperReferenceCandidate{
			RawReference: raw, DOI: "https://doi.org/10.1000/LOCAL", Title: "A local paper", Confidence: 0.9,
		},
	}})
	if err != nil {
		t.Fatalf("MergeExtractedCitations() error = %v", err)
	}
	if len(items) != 1 || items[0].CitedDocumentID == nil || *items[0].CitedDocumentID != target.ID ||
		items[0].ResolutionStatus != types.DocumentStageSucceeded {
		t.Fatalf("local match = %#v, want target %s", items, target.ID)
	}
}

func TestPaperServiceRematchReferenceLinksNewLocalDocument(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, documents, _, _ := newPaperServiceForTest(citing)
	raw := "Ada Lovelace. 2024. A late local paper."
	items, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
		Candidate: &types.PaperReferenceCandidate{
			RawReference: raw, DOI: "10.1000/late", Title: "A late local paper", Confidence: 0.9,
		},
	}})
	if err != nil || len(items) != 1 {
		t.Fatalf("MergeExtractedCitations() = %#v, err=%v", items, err)
	}
	if items[0].CitedDocumentID != nil {
		t.Fatal("reference unexpectedly resolved before the target was imported")
	}
	target := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, DOI: "10.1000/late"}
	if _, err := documents.Create(context.Background(), target); err != nil {
		t.Fatalf("create late target: %v", err)
	}

	updated, linked, err := service.RematchReference(context.Background(), items[0].ReferenceID.String())
	if err != nil {
		t.Fatalf("RematchReference() error = %v", err)
	}
	if !linked || updated.CitedDocumentID == nil || *updated.CitedDocumentID != target.ID {
		t.Fatalf("rematch = %#v linked=%t, want target %s", updated, linked, target.ID)
	}
	if updated.ResolutionStatus != types.DocumentStageSucceeded {
		t.Fatalf("resolution status = %q", updated.ResolutionStatus)
	}

	again, linkedAgain, err := service.RematchReference(context.Background(), items[0].ReferenceID.String())
	if err != nil || linkedAgain || again.CitedDocumentID == nil || *again.CitedDocumentID != target.ID {
		t.Fatalf("second rematch = %#v linked=%t err=%v", again, linkedAgain, err)
	}
}

func TestPaperServiceRematchReferenceUsesRawDOIFallback(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, documents, _, _ := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Ada Lovelace. 2024. A raw-only paper. https://doi.org/10.1000/raw-only",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	target := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, DOI: "10.1000/raw-only"}
	if _, err := documents.Create(context.Background(), target); err != nil {
		t.Fatalf("create raw-only target: %v", err)
	}

	updated, linked, err := service.RematchReference(context.Background(), citation.ReferenceID.String())
	if err != nil {
		t.Fatalf("RematchReference() error = %v", err)
	}
	if !linked || updated.CitedDocumentID == nil || *updated.CitedDocumentID != target.ID {
		t.Fatalf("raw-only rematch = %#v linked=%t, want target %s", updated, linked, target.ID)
	}
}

func TestPaperServiceRematchReferenceExcludesCitingDocument(t *testing.T) {
	citing := types.Document{
		ID: uuid.New(), SourceState: types.DocumentSourceAvailable, DOI: "10.1000/self",
	}
	service, _, _, _ := newPaperServiceForTest(citing)
	raw := "Self Author. 2024. Self paper."
	items, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
		Candidate: &types.PaperReferenceCandidate{
			RawReference: raw, DOI: "10.1000/self", Title: "Self paper", Confidence: 0.9,
		},
	}})
	if err != nil || len(items) != 1 {
		t.Fatalf("MergeExtractedCitations() = %#v, err=%v", items, err)
	}

	updated, linked, err := service.RematchReference(context.Background(), items[0].ReferenceID.String())
	if err != nil {
		t.Fatalf("RematchReference() error = %v", err)
	}
	if linked || updated.CitedDocumentID != nil {
		t.Fatalf("self rematch = %#v linked=%t, want unresolved", updated, linked)
	}
}

// P0-3 regression: resolving a work for paper A must not create a shared
// tombstone that prevents paper B from adding the same raw reference.
func TestPaperServiceResolvedReferenceDoesNotBlockSameRawReferenceInAnotherPaper(t *testing.T) {
	citingA := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	citingB := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	target := types.Document{
		ID: uuid.New(), SourceState: types.DocumentSourceAvailable, DOI: "10.1000/shared-target",
	}
	service, _, _, _ := newPaperServiceForTest(citingA, citingB, target)
	raw := "Ada Lovelace. 2024. Shared bibliography entry."
	first, err := service.MergeExtractedCitations(context.Background(), citingA.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
		Candidate: &types.PaperReferenceCandidate{
			RawReference: raw, DOI: "10.1000/shared-target", Title: "Shared bibliography entry", Confidence: 0.9,
		},
	}})
	if err != nil || len(first) != 1 {
		t.Fatalf("paper A merge = %#v, err=%v", first, err)
	}
	if first[0].CitedDocumentID == nil || *first[0].CitedDocumentID != target.ID {
		t.Fatalf("paper A citation = %#v, want target %s", first[0], target.ID)
	}

	second, err := service.AddCitation(context.Background(), citingB.ID.String(), types.PaperCitationInput{
		RawReference: raw,
	})
	if err != nil {
		t.Fatalf("paper B add after A resolved: %v", err)
	}
	if second.ReferenceID == first[0].ReferenceID {
		t.Fatal("same raw reference in another paper reused A's row")
	}
	resolved, err := service.ResolveReference(
		context.Background(), second.ReferenceID.String(), target.ID.String(),
	)
	if err != nil {
		t.Fatalf("resolve paper B reference: %v", err)
	}
	if resolved.CitedDocumentID == nil || *resolved.CitedDocumentID != target.ID {
		t.Fatalf("paper B resolution = %#v", resolved)
	}
	stillA, err := service.GetReference(context.Background(), first[0].ReferenceID.String())
	if err != nil || stillA.CitedDocumentID == nil || *stillA.CitedDocumentID != target.ID {
		t.Fatalf("paper A row changed = %#v, err=%v", stillA, err)
	}

	if err := service.RemoveCitation(context.Background(), citingB.ID.String(), second.ReferenceID.String()); err != nil {
		t.Fatalf("remove paper B reference: %v", err)
	}
	readded, err := service.AddCitation(context.Background(), citingB.ID.String(), types.PaperCitationInput{
		RawReference: raw,
	})
	if err != nil {
		t.Fatalf("re-add paper B reference: %v", err)
	}
	if readded.ReferenceID != second.ReferenceID {
		t.Fatalf("re-added reference id = %s, want %s", readded.ReferenceID, second.ReferenceID)
	}
}

func TestPaperServiceReferenceRowsAreScopedToCitingPaper(t *testing.T) {
	first := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	second := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, _ := newPaperServiceForTest(first, second)
	raw := "Shared raw reference"

	firstItems, err := service.MergeExtractedCitations(context.Background(), first.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
	}})
	if err != nil || len(firstItems) != 1 {
		t.Fatalf("first merge = %#v, err=%v", firstItems, err)
	}
	secondItems, err := service.MergeExtractedCitations(context.Background(), second.ID.String(), []types.PaperCitationInput{{
		RawReference: raw,
	}})
	if err != nil || len(secondItems) != 1 {
		t.Fatalf("second merge = %#v, err=%v", secondItems, err)
	}
	if firstItems[0].ReferenceID == secondItems[0].ReferenceID {
		t.Fatal("the same raw reference in two papers must keep independent rows")
	}
}

func TestPaperServiceMergeExtractedCitationsDoesNotRemoveManualEdge(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	manualTarget := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, _ := newPaperServiceForTest(citing, manualTarget)
	if _, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		CitedDocumentID: &manualTarget.ID,
		RawReference:    "Curated citation",
	}); err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}

	items, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), nil)
	if err != nil {
		t.Fatalf("MergeExtractedCitations() error = %v", err)
	}
	if len(items) != 1 || items[0].CitedDocumentID == nil || *items[0].CitedDocumentID != manualTarget.ID ||
		items[0].EffectiveOrigin() != types.PaperCitationOriginManual {
		t.Fatalf("manual citation was not retained: %#v", items)
	}
}

func TestPaperServiceNormalizesMetadataUpdateAndSearch(t *testing.T) {
	document := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, metadata, _ := newPaperServiceForTest(document)
	title := "  Example Paper  "
	authors := []types.PaperAuthor{{Name: "  Alice  ", Affiliation: "  Lab  "}}
	tags := []string{" AI ", "ai", "Systems"}

	updated, err := service.UpdateMetadata(
		context.Background(),
		document.ID.String(),
		types.PaperMetadataUpdate{Title: &title, Authors: &authors, Tags: &tags},
	)
	if err != nil {
		t.Fatalf("UpdateMetadata() error = %v", err)
	}
	if updated.Title != "Example Paper" {
		t.Fatalf("title = %q, want normalized title", updated.Title)
	}
	if len(updated.Authors) != 1 ||
		updated.Authors[0].Name != "Alice" ||
		updated.Authors[0].Affiliation != "Lab" {
		t.Fatalf("authors = %#v, want normalized author", updated.Authors)
	}
	if len(updated.Tags) != 2 || updated.Tags[0] != "ai" || updated.Tags[1] != "systems" {
		t.Fatalf("tags = %#v, want normalized unique tags", updated.Tags)
	}
	if updated.MetadataSource != types.PaperMetadataSourceManual {
		t.Fatalf("metadata source = %q, want manual", updated.MetadataSource)
	}

	_, err = service.SearchMetadata(context.Background(), types.PaperSearch{
		Title:    "  query  ",
		Author:   "  Alice ",
		Tags:     []string{" AI ", "ai"},
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("SearchMetadata() error = %v", err)
	}
	if metadata.lastSearch.Title != "query" ||
		metadata.lastSearch.Author != "Alice" ||
		len(metadata.lastSearch.Tags) != 1 ||
		metadata.lastSearch.Tags[0] != "ai" ||
		metadata.lastSearch.TagMode != types.PaperTagModeAll {
		t.Fatalf("normalized search = %#v", metadata.lastSearch)
	}
}
