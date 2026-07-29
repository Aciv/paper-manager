package service

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type paperDocumentRepoStub struct {
	documents     map[uuid.UUID]types.Document
	markdowns     map[uuid.UUID]types.DocumentMarkdown
	blocks        map[uuid.UUID][]types.Block
	ensureCreated int
	referenced    func(uuid.UUID) bool
	search        func(types.DocumentSearch) (types.DocumentPage, error)
}

func newPaperDocumentRepoStub(documents ...types.Document) *paperDocumentRepoStub {
	repo := &paperDocumentRepoStub{
		documents: make(map[uuid.UUID]types.Document),
		markdowns: make(map[uuid.UUID]types.DocumentMarkdown),
		blocks:    make(map[uuid.UUID][]types.Block),
	}
	for _, document := range documents {
		repo.documents[document.ID] = document
	}
	return repo
}

func (r *paperDocumentRepoStub) Create(
	_ context.Context, document types.Document,
) (types.Document, error) {
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
	return document, nil
}

func (r *paperDocumentRepoStub) EnsureUnknown(
	_ context.Context, id uuid.UUID,
) (types.Document, bool, error) {
	if document, ok := r.documents[id]; ok {
		if document.Status != types.DocumentStatusUnknown {
			return types.Document{}, false, types.ErrUnknownDocumentConflict
		}
		return document, false, nil
	}
	document := types.Document{
		ID:          id,
		ContentType: types.UnknownDocumentContentType,
		StorageKey:  types.UnknownDocumentStorageScheme + id.String(),
		Status:      types.DocumentStatusUnknown,
	}
	r.documents[id] = document
	r.ensureCreated++
	return document, true, nil
}

func (r *paperDocumentRepoStub) DeleteUnknownIfUnreferenced(
	_ context.Context, id uuid.UUID,
) (bool, error) {
	document, ok := r.documents[id]
	if !ok || document.Status != types.DocumentStatusUnknown {
		return false, nil
	}
	if r.referenced != nil && r.referenced(id) {
		return false, nil
	}
	delete(r.documents, id)
	return true, nil
}

func (r *paperDocumentRepoStub) Search(
	_ context.Context, search types.DocumentSearch,
) (types.DocumentPage, error) {
	if r.search != nil {
		return r.search(search)
	}
	return types.DocumentPage{}, nil
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
	context.Context, types.DocumentMarkdown, []types.Block,
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

func (r *paperDocumentRepoStub) UpdateStatus(
	_ context.Context, id uuid.UUID, status types.DocumentStatus, message *string,
) error {
	document, ok := r.documents[id]
	if !ok {
		return sql.ErrNoRows
	}
	document.Status = status
	document.ErrorMessage = message
	r.documents[id] = document
	return nil
}

func (r *paperDocumentRepoStub) UpdateStatusIfCurrent(
	_ context.Context,
	id uuid.UUID,
	status types.DocumentStatus,
	message *string,
	expected []types.DocumentStatus,
) (bool, error) {
	document, ok := r.documents[id]
	if !ok {
		return false, nil
	}
	for _, candidate := range expected {
		if document.Status == candidate {
			document.Status = status
			document.ErrorMessage = message
			r.documents[id] = document
			return true, nil
		}
	}
	return false, nil
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
	items map[[2]uuid.UUID]types.PaperCitation
}

func newPaperCitationRepoStub() *paperCitationRepoStub {
	return &paperCitationRepoStub{
		items: make(map[[2]uuid.UUID]types.PaperCitation),
	}
}

func citationKey(citingDocumentID, citedDocumentID uuid.UUID) [2]uuid.UUID {
	return [2]uuid.UUID{citingDocumentID, citedDocumentID}
}

func (r *paperCitationRepoStub) AddCitation(
	_ context.Context, citation types.PaperCitation,
) error {
	if err := citation.Validate(); err != nil {
		return err
	}
	r.items[citationKey(citation.CitingDocumentID, citation.CitedDocumentID)] = citation
	return nil
}

func (r *paperCitationRepoStub) ReplaceCitations(
	_ context.Context,
	citingDocumentID uuid.UUID,
	citations []types.PaperCitation,
) error {
	for key := range r.items {
		if key[0] == citingDocumentID {
			delete(r.items, key)
		}
	}
	for _, citation := range citations {
		if err := citation.Validate(); err != nil {
			return err
		}
		r.items[citationKey(citingDocumentID, citation.CitedDocumentID)] = citation
	}
	return nil
}

func (r *paperCitationRepoStub) RemoveCitation(
	_ context.Context, citingDocumentID, citedDocumentID uuid.UUID,
) error {
	key := citationKey(citingDocumentID, citedDocumentID)
	if _, ok := r.items[key]; !ok {
		return sql.ErrNoRows
	}
	delete(r.items, key)
	return nil
}

func (r *paperCitationRepoStub) RepointCitation(
	_ context.Context,
	citingDocumentID, oldCitedDocumentID, newCitedDocumentID uuid.UUID,
	rawReference string,
) error {
	oldKey := citationKey(citingDocumentID, oldCitedDocumentID)
	if _, ok := r.items[oldKey]; !ok {
		return sql.ErrNoRows
	}
	delete(r.items, oldKey)
	replacement := types.PaperCitation{
		CitingDocumentID: citingDocumentID,
		CitedDocumentID:  newCitedDocumentID,
		RawReference:     rawReference,
	}
	if err := replacement.Validate(); err != nil {
		return err
	}
	r.items[citationKey(citingDocumentID, newCitedDocumentID)] = replacement
	return nil
}

func (r *paperCitationRepoStub) ListByCitingDocumentID(
	_ context.Context, citingDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	items := make([]types.PaperCitation, 0)
	for key, citation := range r.items {
		if key[0] == citingDocumentID {
			items = append(items, citation)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CitedDocumentID.String() < items[j].CitedDocumentID.String()
	})
	return items, nil
}

func (r *paperCitationRepoStub) ListByCitedDocumentID(
	_ context.Context, citedDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	items := make([]types.PaperCitation, 0)
	for key, citation := range r.items {
		if key[1] == citedDocumentID {
			items = append(items, citation)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CitingDocumentID.String() < items[j].CitingDocumentID.String()
	})
	return items, nil
}

func (r *paperCitationRepoStub) references(documentID uuid.UUID) bool {
	for key := range r.items {
		if key[0] == documentID || key[1] == documentID {
			return true
		}
	}
	return false
}

func newPaperServiceForTest(
	documents ...types.Document,
) (*paperService, *paperDocumentRepoStub, *paperMetadataRepoStub, *paperCitationRepoStub) {
	documentRepo := newPaperDocumentRepoStub(documents...)
	metadataRepo := newPaperMetadataRepoStub()
	citationRepo := newPaperCitationRepoStub()
	documentRepo.referenced = citationRepo.references
	service := NewPaperService(documentRepo, metadataRepo, citationRepo).(*paperService)
	return service, documentRepo, metadataRepo, citationRepo
}

func TestPaperServiceRejectsMissingCitationEndpoint(t *testing.T) {
	citing := types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}
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
	document := types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}
	service, _, _, _ := newPaperServiceForTest(document)

	_, err := service.AddCitation(context.Background(), document.ID.String(), types.PaperCitationInput{
		CitedDocumentID: &document.ID,
		RawReference:    "Self reference.",
	})
	if !errors.Is(err, types.ErrSelfPaperCitation) {
		t.Fatalf("AddCitation() error = %v, want ErrSelfPaperCitation", err)
	}
}

func TestPaperServiceReusesUnknownPlaceholderAndPreservesRawReference(t *testing.T) {
	citing := types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}
	service, documents, _, citations := newPaperServiceForTest(citing)
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
	if first.CitedDocumentID != second.CitedDocumentID {
		t.Fatalf("placeholder IDs differ: %s != %s", first.CitedDocumentID, second.CitedDocumentID)
	}
	if documents.ensureCreated != 1 {
		t.Fatalf("unknown documents created = %d, want 1", documents.ensureCreated)
	}
	if len(citations.items) != 1 {
		t.Fatalf("citation count = %d, want 1", len(citations.items))
	}
	if first.RawReference != rawReference {
		t.Fatalf("raw reference = %q, want original %q", first.RawReference, rawReference)
	}
	if documents.documents[first.CitedDocumentID].Status != types.DocumentStatusUnknown {
		t.Fatal("citation target is not an unknown placeholder")
	}
}

func TestPaperServiceResolveCitationRepointsAndCleansPlaceholder(t *testing.T) {
	citing := types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}
	resolved := types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}
	service, documents, _, _ := newPaperServiceForTest(citing, resolved)
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
	if resolvedCitation.CitedDocumentID != resolved.ID {
		t.Fatalf("resolved target = %s, want %s", resolvedCitation.CitedDocumentID, resolved.ID)
	}
	if _, ok := documents.documents[unresolved.CitedDocumentID]; ok {
		t.Fatal("unreferenced unknown placeholder was not deleted")
	}

	forward, err := service.ListCitations(context.Background(), citing.ID.String())
	if err != nil {
		t.Fatalf("ListCitations() error = %v", err)
	}
	reverse, err := service.ListCitedBy(context.Background(), resolved.ID.String())
	if err != nil {
		t.Fatalf("ListCitedBy() error = %v", err)
	}
	if len(forward) != 1 || forward[0].CitedDocumentID != resolved.ID {
		t.Fatalf("forward citations = %#v", forward)
	}
	if len(reverse) != 1 || reverse[0].CitingDocumentID != citing.ID {
		t.Fatalf("reverse citations = %#v", reverse)
	}
}

func TestPaperServiceNormalizesMetadataUpdateAndSearch(t *testing.T) {
	document := types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}
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
