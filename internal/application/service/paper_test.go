package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"io"
	"math"
	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from paper_test.go.
// ---------------------------------------------------------------------------

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

func (r *paperDocumentRepoStub) FinishStage(_ context.Context, id uuid.UUID, stage types.DocumentStage, generation int64, status types.DocumentStageStatus, errorCode, errorMessage string) (types.DocumentStageState, bool, error) {
	state, applied, err := r.transitionStage(id, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning}, status)
	if err != nil || !applied {
		return state, applied, err
	}
	document := r.documents[id]
	if errorCode != "" {
		state.ErrorCode = &errorCode
	}
	if errorMessage != "" {
		state.ErrorMessage = &errorMessage
	}
	setStubDocumentStage(&document, state)
	r.documents[id] = document
	return state, true, nil
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

func (r *paperDocumentRepoStub) GetMany(
	_ context.Context, ids []uuid.UUID,
) ([]types.Document, error) {
	documents := make([]types.Document, 0, len(ids))
	for _, id := range ids {
		document, ok := r.documents[id]
		if !ok {
			continue
		}
		document = normalizeTestDocumentSource(document)
		r.documents[id] = document
		documents = append(documents, document)
	}
	return documents, nil
}

func (r *paperDocumentRepoStub) CitationTargetProgressBatch(
	_ context.Context, ids []uuid.UUID,
) (map[uuid.UUID]types.PaperReferenceProgress, error) {
	return make(map[uuid.UUID]types.PaperReferenceProgress, len(ids)), nil
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

func (r *paperDocumentRepoStub) FindAvailableByDOIs(
	_ context.Context, dois []string, excludeDocumentID uuid.UUID,
) (map[string]uuid.UUID, error) {
	wanted := make(map[string]struct{}, len(dois))
	for _, doi := range dois {
		wanted[strings.ToLower(strings.TrimSpace(doi))] = struct{}{}
	}
	matches := make(map[string]uuid.UUID, len(dois))
	for id, stored := range r.documents {
		document := normalizeTestDocumentSource(stored)
		r.documents[id] = document
		doi := strings.ToLower(strings.TrimSpace(document.DOI))
		if _, ok := wanted[doi]; !ok || doi == "" ||
			document.SourceState != types.DocumentSourceAvailable || document.ID == excludeDocumentID {
			continue
		}
		matches[doi] = document.ID
	}
	return matches, nil
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

func (r *paperMetadataRepoStub) GetByDocumentIDs(
	_ context.Context, documentIDs []uuid.UUID,
) (map[uuid.UUID]types.PaperMetadata, error) {
	result := make(map[uuid.UUID]types.PaperMetadata, len(documentIDs))
	for _, id := range documentIDs {
		if metadata, ok := r.items[id]; ok {
			result[id] = metadata
		}
	}
	return result, nil
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

func (r *paperMetadataRepoStub) SearchTitleSeeds(
	_ context.Context, seeds []string, excludeDocumentID uuid.UUID, perSeedLimit int,
) (map[string][]types.PaperMetadata, error) {
	if perSeedLimit <= 0 {
		perSeedLimit = 10
	}
	matches := make(map[string][]types.PaperMetadata, len(seeds))
	for _, seed := range seeds {
		for _, metadata := range r.items {
			if metadata.DocumentID == excludeDocumentID || len(matches[seed]) >= perSeedLimit {
				continue
			}
			if strings.Contains(strings.ToLower(metadata.Title), strings.ToLower(seed)) {
				matches[seed] = append(matches[seed], metadata)
			}
		}
	}
	return matches, nil
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

func (r *paperCitationRepoStub) UpdateReferenceSource(
	_ context.Context, id uuid.UUID, edit types.PaperReferenceSourceEdit,
) (types.PaperReference, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, sql.ErrNoRows
	}
	if edit.DOI != nil {
		citation.DOI = *edit.DOI
	}
	if edit.Title != nil {
		citation.Title = *edit.Title
	}
	if edit.Authors != nil {
		citation.Authors = *edit.Authors
		if citation.Authors == nil {
			citation.Authors = []types.PaperAuthor{}
		}
	}
	if edit.Venue != nil {
		citation.Venue = *edit.Venue
	}
	if edit.PublishedAtSet {
		citation.PublishedAt = edit.PublishedAt
	}
	if edit.Volume != nil {
		citation.Volume = *edit.Volume
	}
	if edit.Issue != nil {
		citation.Issue = *edit.Issue
	}
	if edit.Pages != nil {
		citation.Pages = *edit.Pages
	}
	if edit.SourceURL != nil {
		citation.SourceURL = *edit.SourceURL
	}
	citation.MetadataSource = types.PaperMetadataSourceManual
	citation.Origin = types.PaperCitationOriginManual
	if citation.CitedDocumentID == nil {
		citation.ResolutionGeneration++
		citation.ResolutionStatus = types.DocumentStageNotStarted
		citation.ResolutionProvider = ""
		citation.ResolutionErrorCode = nil
		citation.ResolutionErrorMsg = nil
	}
	r.items[id] = citation
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

func (r *paperCitationRepoStub) QueueDownload(
	_ context.Context, id uuid.UUID, attempt uuid.UUID,
) (types.PaperReference, bool, error) {
	citation, ok := r.items[id]
	if !ok {
		return types.PaperReference{}, false, sql.ErrNoRows
	}
	if citation.CitedDocumentID != nil {
		return citation.PaperReference, false, nil
	}
	failed := citation.DownloadErrorCode != nil || citation.DownloadErrorMessage != nil
	if citation.DownloadRequestedAt != nil && !failed &&
		time.Since(*citation.DownloadRequestedAt) < types.ReferenceDownloadInFlightWindow {
		return citation.PaperReference, false, types.ErrReferenceDownloadInProgress
	}
	now := time.Now().UTC()
	citation.DownloadAttempt = &attempt
	citation.DownloadRequestedAt = &now
	citation.DownloadErrorCode = nil
	citation.DownloadErrorMessage = nil
	citation.DownloadUpdatedAt = &now
	r.items[id] = citation
	return citation.PaperReference, true, nil
}

func (r *paperCitationRepoStub) FailDownload(
	_ context.Context, id uuid.UUID, attempt uuid.UUID, errorCode, errorMessage string,
) (bool, error) {
	citation, ok := r.items[id]
	if !ok {
		return false, sql.ErrNoRows
	}
	if citation.CitedDocumentID != nil || citation.DownloadAttempt == nil || *citation.DownloadAttempt != attempt {
		return false, nil
	}
	now := time.Now().UTC()
	citation.DownloadErrorCode = &errorCode
	citation.DownloadErrorMessage = &errorMessage
	citation.DownloadUpdatedAt = &now
	r.items[id] = citation
	return true, nil
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

// TestPaperServiceRejectsMissingCitationEndpoint verifies that AddCitation with a cited
// document ID that does not exist fails with sql.ErrNoRows and persists no row.
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

// TestPaperServiceRejectsSelfCitation verifies that AddCitation refuses an edge from a
// document to itself and returns ErrSelfPaperCitation.
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

// TestPaperServiceReusesReferenceAndPreservesRawReference verifies that adding the same
// raw reference twice keeps one unresolved row with a stable ID and the original text.
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

// TestPaperServiceStoresStructuredCandidateOnReference verifies that a structured
// candidate's fields, including an arXiv-derived source URL, are persisted on the row.
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

// TestPaperServiceResolveCitationLinksSharedReference verifies that resolving a raw
// reference links the existing row without creating a document and is visible from both sides.
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

// TestPaperServiceMergeExtractedCitationsRetainsResolvedReference verifies that a
// re-extracted variant of a resolved reference keeps its row, link, and extraction origin.
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

// TestPaperServiceMergeExtractedCitationsLinksLocalDOI verifies that a URL-form DOI
// candidate is normalized and immediately matched to the local document that owns it.
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

// TestPaperServiceRematchReferenceLinksNewLocalDocument verifies that RematchReference
// links a previously unresolved row once its DOI target is imported, and is idempotent.
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

// TestPaperServiceRematchReferenceUsesRawDOIFallback verifies that rematching can resolve
// a row using the DOI embedded in its raw text when no structured DOI is stored.
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

// TestPaperServiceRematchReferenceExcludesCitingDocument verifies that a reference whose
// DOI matches the citing document is left unresolved rather than linked to itself.
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

// TestPaperServiceReferenceRowsAreScopedToCitingPaper verifies that the same raw reference
// extracted for two papers produces two independent rows with different IDs.
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

// TestPaperServiceMergeExtractedCitationsMatchesBibliographyInBatches verifies that one merge
// resolves references by DOI and title/author/year, leaving entries without a target unresolved.
func TestPaperServiceMergeExtractedCitationsMatchesBibliographyInBatches(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	doiTarget := types.Document{
		ID: uuid.New(), SourceState: types.DocumentSourceAvailable, DOI: "10.1000/batch-doi",
	}
	titleTarget := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, metadata, _ := newPaperServiceForTest(citing, doiTarget, titleTarget)
	published := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	metadata.items[doiTarget.ID] = types.PaperMetadata{
		DocumentID: doiTarget.ID, Title: "DOI target",
	}
	metadata.items[titleTarget.ID] = types.PaperMetadata{
		DocumentID: titleTarget.ID, Title: "Batched Local Matching",
		Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}}, PublishedAt: &published,
	}

	items, err := service.MergeExtractedCitations(context.Background(), citing.ID.String(), []types.PaperCitationInput{
		{
			RawReference: "Reference resolved by DOI",
			Candidate: &types.PaperReferenceCandidate{
				RawReference: "Reference resolved by DOI", DOI: "10.1000/batch-doi",
			},
		},
		{
			RawReference: "Reference resolved by title",
			Candidate: &types.PaperReferenceCandidate{
				RawReference: "Reference resolved by title", Title: "Batched Local Matching",
				Authors: []string{"Ada Lovelace"}, PublishedYear: 2020,
			},
		},
		{
			RawReference: "Reference with no local target",
			Candidate: &types.PaperReferenceCandidate{
				RawReference: "Reference with no local target", Title: "Unrelated Work",
			},
		},
	})
	if err != nil {
		t.Fatalf("MergeExtractedCitations() error = %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("citations = %#v, want three rows", items)
	}
	byRaw := make(map[string]types.PaperCitation, len(items))
	for _, item := range items {
		byRaw[item.RawReference] = item
	}
	if doi := byRaw["Reference resolved by DOI"]; doi.CitedDocumentID == nil || *doi.CitedDocumentID != doiTarget.ID ||
		doi.ResolutionStatus != types.DocumentStageSucceeded {
		t.Fatalf("DOI citation = %#v, want target %s", doi, doiTarget.ID)
	}
	if title := byRaw["Reference resolved by title"]; title.CitedDocumentID == nil || *title.CitedDocumentID != titleTarget.ID ||
		title.ResolutionStatus != types.DocumentStageSucceeded {
		t.Fatalf("title citation = %#v, want target %s", title, titleTarget.ID)
	}
	if unmatched := byRaw["Reference with no local target"]; unmatched.CitedDocumentID != nil {
		t.Fatalf("unmatched citation = %#v, want unresolved", unmatched)
	}
}

// TestPaperServiceMergeExtractedCitationsDoesNotRemoveManualEdge verifies that an empty
// extraction merge retains a manually added citation and its manual provenance.
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

// TestPaperServiceNormalizesMetadataUpdateAndSearch verifies that metadata updates and
// searches trim text, deduplicate tags, and mark manual provenance.
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

// TestPaperServiceSearchMetadataRejectsSingleASCIICharacterTerms verifies that
// one-character ASCII queries are rejected while a single CJK character is accepted.
func TestPaperServiceSearchMetadataRejectsSingleASCIICharacterTerms(t *testing.T) {
	document := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, _ := newPaperServiceForTest(document)

	for _, search := range []types.PaperSearch{
		{Title: " a ", Page: 1, PageSize: 10},
		{Author: "X", Page: 1, PageSize: 10},
	} {
		if _, err := service.SearchMetadata(context.Background(), search); !errors.Is(err, ErrInvalidPaperMetadata) {
			t.Fatalf("SearchMetadata(%#v) error = %v, want ErrInvalidPaperMetadata", search, err)
		}
	}
	// Single CJK characters are legitimate surname/title queries and must not
	// be rejected by the ASCII short-term guard.
	if _, err := service.SearchMetadata(context.Background(), types.PaperSearch{
		Author: "王", Page: 1, PageSize: 10,
	}); err != nil {
		t.Fatalf("SearchMetadata() single CJK author error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_author_sanitize_test.go.
// ---------------------------------------------------------------------------

// TestPaperCatalogAuthorNamesDropsLayoutNoise verifies that catalog author extraction
// strips markers, drops layout and HTML noise, and deduplicates names.
func TestPaperCatalogAuthorNamesDropsLayoutNoise(t *testing.T) {
	got := paperCatalogAuthorNames([]types.PaperAuthor{
		{Name: "Zhen Li∗†"},
		{Name: "Huazhong University of Science and"},
		{Name: "3"},
		{Name: `<div class="left">`},
		{Name: "Yao Wan"},
		{Name: "Yao Wan"},
	})
	want := []string{"Zhen Li", "Yao Wan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paperCatalogAuthorNames() = %#v, want %#v", got, want)
	}
}

// TestExtractionMetadataUpdateSanitizesAuthors verifies that extracted author candidates
// are filtered for layout noise and duplicates before becoming a metadata update.
func TestExtractionMetadataUpdateSanitizesAuthors(t *testing.T) {
	update := extractionMetadataUpdate(&types.PaperExtractionResult{
		Authors: []types.PaperTextCandidate{
			{Value: "Alice Zhang"},
			{Value: "3"},
			{Value: "<ul>"},
			{Value: "Tsinghua University"},
			{Value: "alice zhang"},
		},
	})
	if update.Authors == nil {
		t.Fatal("expected sanitized authors")
	}
	got := *update.Authors
	if len(got) != 1 || got[0].Name != "Alice Zhang" {
		t.Fatalf("authors = %#v, want [Alice Zhang]", got)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_info_test.go.
// ---------------------------------------------------------------------------

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

// TestPaperInfoExtractMetadataPersistsCurrentSnapshot verifies that ExtractMetadata writes
// normalized metadata and deduplicated references from the current markdown snapshot.
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

// TestPaperInfoExtractMetadataQueuesIndependentDownstreamStages verifies that a successful
// extraction finishes its stage and queues fresh source and reference lookups.
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

// TestPaperInfoExtractionRetryStartsFreshSourceLookupGeneration verifies that restarting
// extraction requeues source lookup in a new generation instead of keeping its prior success.
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

// TestSourceCatalogCandidateOverridesExtractionButNotStrongerSources verifies that a catalog
// candidate replaces unknown or extraction metadata but never outranks manual metadata.
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

// TestSourceCatalogCandidateMergeableUsesTitleAndAuthors verifies that closely related titles
// with matching authors can merge, while unrelated authors or extraction-owned metadata cannot.
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

// TestPaperSourceQueryApplyMergesMissingCatalogFields verifies that applying a catalog
// candidate stores the DOI and fills only missing fields, preserving populated metadata.
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

// TestPaperInfoExtractMetadataDoesNotClearExistingMetadataForEmptyResult verifies that an
// empty extraction result leaves manual metadata and existing citations untouched.
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

// TestPaperInfoExtractMetadataClearsOnlyAutomaticCitationsForDetectedEmptyBibliography
// verifies that a detected but empty bibliography still triggers an empty automatic-citation merge.
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

// TestExtractionCitationInputsPreserveBibliographyOrderAcrossSkippedRows verifies that
// blank entries are dropped while surviving references keep their original bibliography positions.
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

// TestExtractionMetadataUpdateOnlyIncludesUsableCandidates verifies that blank values are
// omitted, NUL bytes stripped, and accepted fields marked with extraction provenance.
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

// TestExtractionCitationInputsRemovePostgresNULBytes verifies that NUL bytes are stripped
// from raw text, candidate titles, and author names before persistence.
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

// TestCatalogMetadataUpdateOmitsEmptyAutomaticFields verifies that a sparse catalog
// candidate produces a patch containing only populated fields with catalog provenance.
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

// TestPaperInfoExtractMetadataRejectsSourceDeletingDuringRPC verifies that a document
// entering deletion during extraction yields ErrStalePaperInfo and no metadata writes.
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

// TestPaperInfoExtractMetadataRejectsCancelledGenerationAfterRPC verifies that cancelling
// the stage during extraction revokes the task without persisting metadata or citations.
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

// TestPaperInfoReferenceDispatcherQueuesBoundedReferences verifies that reference dispatch
// submits each eligible reference of a document, within the configured cap, at generation 1.
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

// TestPaperInfoReferenceDispatcherQueuesEachPapersOwnReference verifies that the same raw
// reference cited by two papers dispatches once per paper and queues both rows.
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

// TestPaperInfoReferenceTaskCarriesOnlyTarget verifies that a source-query enqueue payload's
// Command resolves to a command naming exactly that target document.
func TestPaperInfoReferenceTaskCarriesOnlyTarget(t *testing.T) {
	payload := types.PaperSourceQueryEnqueuePayload{DocumentID: uuid.NewString(), Stage: types.DocumentStageSourceLookup, Generation: 1}
	command, err := payload.Command()
	if err != nil || command.DocumentID.String() != payload.DocumentID {
		t.Fatalf("single-target reference command = %#v, err=%v", command, err)
	}
}

// TestPaperInfoApplySourceKeepsNewestDocumentOnDOIConflict verifies that a catalog DOI owned
// by an older document transfers to the newest upload, which remains the only stored document.
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

// TestPaperInfoApplySourceDeletesOlderCurrentDocument verifies that when the candidate's DOI
// belongs to a newer document, the older upload is deleted and the candidate is not applied.
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

// TestNewestPaperDocumentPrefersStoredSourceOverCreationTime verifies that a document with a
// stored source outranks an unsourced one, falling back to creation time otherwise.
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

// TestPaperInfoApplySourceRetriesDuplicateReplacementRace verifies that a failed duplicate
// replacement is retried and still leaves the newest document holding the transferred DOI.
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

// TestPaperArXivIDAcceptsBareAndWrappedIDsWithoutDamagingLegacyIDs verifies that arXiv
// identifiers are normalized from URL and versioned forms without altering legacy-style IDs.
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

// TestPaperInfoSourceQueryEnqueueSubmitsWithoutMarkdown verifies that source lookup
// dispatches a job from stored metadata even when no markdown content is available.
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

// TestPaperInfoSourceQueryEnqueueRejectsDeletingDocument verifies that enqueueing source
// lookup for a document in the deleting state fails with ErrStalePaperInfo.
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

// TestScorePaperCatalogCandidateUsesBibliographicSignals verifies that title, author, year,
// and venue matches score above the threshold and beat an unrelated candidate.
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

// TestScorePaperCatalogCandidateRejectsConflictingDOIExactArxiv verifies that an arXiv URL
// paired with a conflicting DOI is neither exact nor a match, while the true record scores fully.
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

// TestScorePaperCatalogCandidateDoesNotPenalizeDifferentDOI verifies that a provider DOI
// differing from the query's neither reduces a title match nor counts as exact.
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

// TestScorePaperCatalogCandidateMakesTitleDominant verifies that a strong title match
// outweighs an author-only match and still clears the threshold despite a wrong author.
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

// TestScorePaperCatalogCandidateRequiresAuthorsOnlyForWeakTitle verifies that a weak title
// match clears the threshold only with a matching author and scores zero otherwise.
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

// TestPaperSourceQueryApplyPersistsNoMatchOutcome verifies that a final catalog no-match
// outcome records the source stage as succeeded with the NO_MATCH error code.
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

// ---------------------------------------------------------------------------
// Consolidated from paper_info_control_test.go.
// ---------------------------------------------------------------------------

type paperInfoQueueStub struct {
	tasks []*asynq.Task
	err   error
}

func (q *paperInfoQueueStub) EnqueueContext(
	_ context.Context,
	task *asynq.Task,
	_ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	if q.err != nil {
		return nil, q.err
	}
	return &asynq.TaskInfo{ID: "queued"}, nil
}

type paperInfoInspectorStub struct{ queued map[string]bool }

func (*paperInfoInspectorStub) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (*paperInfoInspectorStub) CancelTasksForStage(context.Context, string, types.DocumentStage, int64) (int, int, error) {
	return 0, 0, nil
}
func (*paperInfoInspectorStub) HasQueuedTasksForDocument(context.Context, string) (bool, error) {
	return false, nil
}
func (s *paperInfoInspectorStub) HasQueuedTaskTypeForDocument(
	_ context.Context,
	_ string,
	taskType string,
) (bool, error) {
	return s.queued[taskType], nil
}

func (*paperInfoInspectorStub) HasQueuedTaskForStage(context.Context, string, types.DocumentStage, int64) (bool, error) {
	return false, nil
}

func (*paperInfoInspectorStub) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (*paperInfoInspectorStub) SnapshotQueuedWork(context.Context) (types.QueuedWorkSnapshot, error) {
	return types.NewQueuedWorkSnapshot(), nil
}

// TestPaperInfoControlRetries verifies that manual extraction and catalog retries enqueue
// fresh-generation tasks that restart source lookup, incrementing on each retry.
func TestPaperInfoControlRetries(t *testing.T) {
	ctx := context.Background()
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/example",
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/paper.md"}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID, MetadataSource: types.PaperMetadataSourceExtraction,
	}
	queue := &paperInfoQueueStub{}
	inspector := &paperInfoInspectorStub{queued: map[string]bool{}}
	controls := newPaperInfoServiceForTest(
		config.Config{},
		documents,
		nil,
		nil,
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref"}}},
		papers,
		queue,
		inspector,
		nil,
	)

	if err := controls.RetryExtraction(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryExtraction() error = %v", err)
	}
	if err := controls.RetryCatalogResolution(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryCatalogResolution() error = %v", err)
	}
	if len(queue.tasks) != 2 || queue.tasks[0].Type() != types.TypePaperMetadataExtract || queue.tasks[1].Type() != types.TypePaperSourceQueryEnqueue {
		t.Fatalf("queued tasks = %#v", queue.tasks)
	}
	var extraction types.PaperMetadataExtractPayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &extraction); err != nil {
		t.Fatal(err)
	}
	var catalog types.PaperSourceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[1].Payload(), &catalog); err != nil {
		t.Fatal(err)
	}
	if extraction.Stage != types.DocumentStageMetadataExtraction || extraction.Generation != 1 ||
		!extraction.RestartSourceLookup ||
		catalog.DocumentID != documentID.String() ||
		catalog.Stage != types.DocumentStageSourceLookup || catalog.Generation != 1 {
		t.Fatalf("manual retries = extraction:%#v catalog:%#v", extraction, catalog)
	}
	document := documents.documents[documentID]
	document.MetadataExtractionStatus = types.DocumentStageFailed
	documents.documents[documentID] = document
	if err := controls.RetryExtraction(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryExtraction(second generation) error = %v", err)
	}
	var secondExtraction types.PaperMetadataExtractPayload
	if err := json.Unmarshal(queue.tasks[2].Payload(), &secondExtraction); err != nil {
		t.Fatal(err)
	}
	if secondExtraction.Generation != 2 || !secondExtraction.RestartSourceLookup {
		t.Fatalf("second user retry generation = %d, want 2", secondExtraction.Generation)
	}
}

// TestPaperInfoTaskIDUsesManualAttempt verifies that managed task IDs differ per manual
// attempt so retried tasks are not deduplicated by the queue.
func TestPaperInfoTaskIDUsesManualAttempt(t *testing.T) {
	base := paperInfoTaskID(types.TypePaperSourceQueryEnqueue, "document-id", 0)
	firstRetry := paperInfoTaskID(types.TypePaperSourceQueryEnqueue, "document-id", 1)
	secondRetry := paperInfoTaskID(types.TypePaperSourceQueryEnqueue, "document-id", 2)
	if base == firstRetry || firstRetry == secondRetry {
		t.Fatalf("task IDs must distinguish manual attempts: %q, %q, %q", base, firstRetry, secondRetry)
	}
}

// TestEnqueueManagedTaskReturnsTaskIDConflict verifies that a task ID conflict from the
// enqueuer is returned unchanged by enqueueManagedTask.
func TestEnqueueManagedTaskReturnsTaskIDConflict(t *testing.T) {
	err := enqueueManagedTask(
		context.Background(),
		&paperInfoQueueStub{err: asynq.ErrTaskIDConflict},
		asynq.NewTask(types.TypePaperSourceQueryEnqueue, nil),
		nil,
		"document-id",
	)
	if !errors.Is(err, asynq.ErrTaskIDConflict) {
		t.Fatalf("enqueueManagedTask() error = %v, want ErrTaskIDConflict", err)
	}
}

// TestEnqueueDocumentStageTaskTreatsSameGenerationConflictAsSuccess verifies that a task ID
// conflict during stage enqueue is treated as a successful handoff.
func TestEnqueueDocumentStageTaskTreatsSameGenerationConflictAsSuccess(t *testing.T) {
	err := enqueueDocumentStageTask(
		context.Background(),
		&paperInfoQueueStub{err: asynq.ErrTaskIDConflict},
		asynq.NewTask(types.TypePaperSourceQueryEnqueue, nil),
		nil,
		"document-id",
	)
	if err != nil {
		t.Fatalf("enqueueDocumentStageTask() error = %v", err)
	}
}

// TestPaperInfoControlRetriesReferenceDispatch verifies that RetryReferenceResolution
// enqueues exactly one reference-lookup task for the document at generation 1.
func TestPaperInfoControlRetriesReferenceDispatch(t *testing.T) {
	ctx := context.Background()
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/paper.md"}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID}
	queue := &paperInfoQueueStub{}
	controls := newPaperInfoServiceForTest(config.Config{}, documents, nil, nil, nil, papers, queue, &paperInfoInspectorStub{queued: map[string]bool{}}, nil)

	if err := controls.RetryReferenceResolution(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryReferenceResolution() error = %v", err)
	}
	if len(queue.tasks) != 1 || queue.tasks[0].Type() != types.TypePaperReferenceQueryEnqueue {
		t.Fatalf("queued tasks = %#v, want one reference dispatch task", queue.tasks)
	}
	var payload types.PaperReferenceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DocumentID != documentID.String() ||
		payload.Stage != types.DocumentStageReferenceLookup || payload.Generation != 1 {
		t.Fatalf("reference retry payload = %#v", payload)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_reference_test.go.
// ---------------------------------------------------------------------------

func referenceSourceText(value string) *string {
	return &value
}

// TestPaperServiceDeleteReferenceRemovesOwnedRow verifies that DeleteReference removes the
// row and a repeated delete reports sql.ErrNoRows.
func TestPaperServiceDeleteReferenceRemovesOwnedRow(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Ada Lovelace. 2024. A removable reference.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}

	if err := service.DeleteReference(context.Background(), citation.ReferenceID.String()); err != nil {
		t.Fatalf("DeleteReference() error = %v", err)
	}
	if len(citations.items) != 0 {
		t.Fatalf("remaining references = %#v", citations.items)
	}
	if err := service.DeleteReference(context.Background(), citation.ReferenceID.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second DeleteReference() error = %v, want sql.ErrNoRows", err)
	}
}

// TestPaperServiceUpdateReferenceSourceNormalizesAndFencesUnresolved verifies that editing
// an unresolved reference normalizes fields, marks it manual, and fences in-flight resolution.
func TestPaperServiceUpdateReferenceSourceNormalizesAndFencesUnresolved(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Original reference text.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	stored := citations.items[citation.ReferenceID]
	stored.ResolutionStatus = types.DocumentStageQueued
	stored.ResolutionGeneration = 3
	stored.ResolutionProvider = "crossref"
	stored.ResolutionErrorCode = referenceSourceText("old_code")
	stored.ResolutionErrorMsg = referenceSourceText("old error")
	citations.items[citation.ReferenceID] = stored

	updated, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), types.PaperReferenceSourceUpdate{
		DOI:         referenceSourceText("https://doi.org/10.1000/ABC."),
		Title:       referenceSourceText("  Corrected title  "),
		Authors:     &[]types.PaperAuthor{{Name: " Ada Lovelace ", Affiliation: " Analytical Engine Lab "}},
		Venue:       referenceSourceText(" NeurIPS "),
		PublishedAt: referenceSourceText("2024-03-02"),
		Volume:      referenceSourceText("12"),
		Issue:       referenceSourceText("3"),
		Pages:       referenceSourceText("1-10"),
		SourceURL:   referenceSourceText(" https://example.org/paper.pdf "),
	})
	if err != nil {
		t.Fatalf("UpdateReferenceSource() error = %v", err)
	}
	if updated.DOI != "10.1000/abc" ||
		updated.Title != "Corrected title" ||
		updated.Venue != "NeurIPS" ||
		updated.Volume != "12" ||
		updated.Issue != "3" ||
		updated.Pages != "1-10" ||
		updated.SourceURL != "https://example.org/paper.pdf" {
		t.Fatalf("updated source = %#v", updated)
	}
	if len(updated.Authors) != 1 ||
		updated.Authors[0].Name != "Ada Lovelace" ||
		updated.Authors[0].Affiliation != "Analytical Engine Lab" {
		t.Fatalf("updated authors = %#v", updated.Authors)
	}
	if updated.PublishedAt == nil || !updated.PublishedAt.Equal(time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("publishedAt = %v", updated.PublishedAt)
	}
	if updated.MetadataSource != types.PaperMetadataSourceManual {
		t.Fatalf("metadata source = %q, want manual", updated.MetadataSource)
	}
	if citations.items[citation.ReferenceID].EffectiveOrigin() != types.PaperCitationOriginManual {
		t.Fatalf(
			"origin = %q, want manual",
			citations.items[citation.ReferenceID].Origin,
		)
	}
	if updated.ResolutionGeneration != 4 ||
		updated.ResolutionStatus != types.DocumentStageNotStarted ||
		updated.ResolutionProvider != "" ||
		updated.ResolutionErrorCode != nil ||
		updated.ResolutionErrorMsg != nil {
		t.Fatalf("resolution fence = %#v", updated)
	}

	cleared, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), types.PaperReferenceSourceUpdate{
		Title:       referenceSourceText(""),
		Authors:     &[]types.PaperAuthor{},
		PublishedAt: referenceSourceText(""),
		SourceURL:   referenceSourceText(""),
	})
	if err != nil {
		t.Fatalf("clear source fields: %v", err)
	}
	if cleared.Title != "" || len(cleared.Authors) != 0 || cleared.PublishedAt != nil || cleared.SourceURL != "" {
		t.Fatalf("cleared source = %#v", cleared)
	}
}

// TestPaperServiceUpdateReferenceSourceKeepsLinkedResolution verifies that editing a
// resolved reference updates its metadata while preserving the link and resolution state.
func TestPaperServiceUpdateReferenceSourceKeepsLinkedResolution(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Linked reference text.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	targetID := uuid.New()
	stored := citations.items[citation.ReferenceID]
	stored.CitedDocumentID = &targetID
	stored.ResolutionStatus = types.DocumentStageSucceeded
	stored.ResolutionGeneration = 7
	citations.items[citation.ReferenceID] = stored

	updated, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), types.PaperReferenceSourceUpdate{
		Title: referenceSourceText("Corrected linked title"),
	})
	if err != nil {
		t.Fatalf("UpdateReferenceSource() error = %v", err)
	}
	if updated.Title != "Corrected linked title" ||
		updated.CitedDocumentID == nil || *updated.CitedDocumentID != targetID ||
		updated.ResolutionStatus != types.DocumentStageSucceeded ||
		updated.ResolutionGeneration != 7 {
		t.Fatalf("linked edit = %#v", updated)
	}
}

// TestPaperServiceUpdateReferenceSourceRejectsInvalidInput verifies that empty, malformed,
// or over-limit edits are rejected with ErrInvalidPaperReference.
func TestPaperServiceUpdateReferenceSourceRejectsInvalidInput(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, _ := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Validated reference text.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	tooManyAuthors := make([]types.PaperAuthor, types.MaxPaperReferenceAuthors+1)
	for index := range tooManyAuthors {
		tooManyAuthors[index] = types.PaperAuthor{Name: "Author"}
	}
	cases := []struct {
		name   string
		update types.PaperReferenceSourceUpdate
	}{
		{name: "empty update", update: types.PaperReferenceSourceUpdate{}},
		{name: "invalid date", update: types.PaperReferenceSourceUpdate{PublishedAt: referenceSourceText("2024-02-31")}},
		{name: "non-http source url", update: types.PaperReferenceSourceUpdate{SourceURL: referenceSourceText("javascript:alert(1)")}},
		{name: "empty author name", update: types.PaperReferenceSourceUpdate{Authors: &[]types.PaperAuthor{{Name: "  "}}}},
		{name: "too many authors", update: types.PaperReferenceSourceUpdate{Authors: &tooManyAuthors}},
		{name: "control character", update: types.PaperReferenceSourceUpdate{Title: referenceSourceText("bad\ntitle")}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), testCase.update)
			if !errors.Is(err, types.ErrInvalidPaperReference) {
				t.Fatalf("error = %v, want ErrInvalidPaperReference", err)
			}
		})
	}
}

func (*paperDocumentRepoStub) QueryBlocks(context.Context, uuid.UUID, types.DocumentBlockQuery) (types.DocumentBlockPage, error) {
	panic("unexpected QueryBlocks call")
}

func (*paperDocumentRepoStub) MarkdownPageCounts(context.Context, []uuid.UUID) (map[uuid.UUID]int, error) {
	panic("unexpected MarkdownPageCounts call")
}
