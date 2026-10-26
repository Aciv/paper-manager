package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"net/http"
	"net/http/httptest"
	"paperMaster/internal/middleware"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Consolidated from bibliography_test.go.
// ---------------------------------------------------------------------------

type bibliographyServiceStub struct {
	result types.BibliographyTask
	err    error
}

func (stub *bibliographyServiceStub) EnqueueBaseBibliography(context.Context, string, string) (types.BibliographyTask, error) {
	return stub.result, stub.err
}

func (stub *bibliographyServiceStub) GetBaseBibliographyTask(context.Context, string, string) (types.BibliographyTask, error) {
	return stub.result, stub.err
}

func (*bibliographyServiceStub) ProcessBaseBibliography(context.Context, *asynq.Task) error {
	return nil
}

// TestBibliographyHandlerAcceptsTask verifies that starting base bibliography
// generation returns 202 with the queued task and its base ID.
func TestBibliographyHandlerAcceptsTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	baseID := uuid.New()
	handler := NewBibliographyHandler(&bibliographyServiceStub{result: types.BibliographyTask{
		ID: "bibliography-" + uuid.NewString(), BaseID: baseID, State: types.BibliographyTaskQueued,
	}})
	engine := gin.New()
	engine.POST("/api/document/bases/:baseID/bibliography", handler.Generate)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/document/bases/"+baseID.String()+"/bibliography", nil)
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Task types.BibliographyTask `json:"task"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Task.State != types.BibliographyTaskQueued || response.Task.BaseID != baseID {
		t.Fatalf("response = %#v", response)
	}
}

// TestBibliographyHandlerReturnsCompletedTask verifies that the status
// endpoint returns the succeeded task including its BibTeX snapshot.
func TestBibliographyHandlerReturnsCompletedTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	baseID := uuid.New()
	taskID := "bibliography-" + uuid.NewString()
	handler := NewBibliographyHandler(&bibliographyServiceStub{result: types.BibliographyTask{
		ID: taskID, BaseID: baseID, State: types.BibliographyTaskSucceeded,
		Snapshot: &types.BibliographySnapshot{BaseID: baseID, BibTeX: "@misc{x}"},
	}})
	engine := gin.New()
	engine.GET("/api/document/bases/:baseID/bibliography/:taskID", handler.Status)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/document/bases/"+baseID.String()+"/bibliography/"+taskID, nil)
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "@misc{x}") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestBibliographyHandlerMapsBaseLimit verifies that generating a bibliography
// for an oversized base maps to 413.
func TestBibliographyHandlerMapsBaseLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewBibliographyHandler(&bibliographyServiceStub{err: types.ErrBibliographyBaseTooLarge})
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/api/document/bases/:baseID/bibliography", handler.Generate)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/document/bases/"+uuid.NewString()+"/bibliography", nil)
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestBibliographyHandlerMapsNotFoundAndOutputLimit verifies that a missing
// base maps to 404 and an oversized generated output maps to 413.
func TestBibliographyHandlerMapsNotFoundAndOutputLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: sql.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "output too large", err: types.ErrBibliographyOutputTooLarge, wantStatus: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewBibliographyHandler(&bibliographyServiceStub{err: test.err})
			engine := gin.New()
			engine.Use(middleware.ErrorHandler())
			engine.POST("/api/document/bases/:baseID/bibliography", handler.Generate)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/document/bases/"+uuid.NewString()+"/bibliography", nil)
			engine.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_citation_test.go.
// ---------------------------------------------------------------------------

type paperCitationHandlerStub struct {
	interfaces.PaperService
	deletedReferenceID string
	updatedReferenceID string
	updatedSource      types.PaperReferenceSourceUpdate
	reference          types.PaperReference
	err                error
}

func (s *paperCitationHandlerStub) DeleteReference(_ context.Context, referenceID string) error {
	s.deletedReferenceID = referenceID
	return s.err
}

func (s *paperCitationHandlerStub) UpdateReferenceSource(
	_ context.Context,
	referenceID string,
	update types.PaperReferenceSourceUpdate,
) (*types.PaperReference, error) {
	s.updatedReferenceID = referenceID
	s.updatedSource = update
	if s.err != nil {
		return nil, s.err
	}
	reference := s.reference
	reference.ReferenceID = uuid.MustParse(referenceID)
	return &reference, nil
}

// TestPaperCitationHandlerDeleteReference verifies that deleting a reference
// by ID returns 204, rejects malformed IDs with 400, and maps unknown rows to 404.
func TestPaperCitationHandlerDeleteReference(t *testing.T) {
	gin.SetMode(gin.TestMode)
	referenceID := uuid.New()
	service := &paperCitationHandlerStub{}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.DELETE("/api/references/:referenceID", NewPaperCitationHandler(service).DeleteReference)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete, "/api/references/"+referenceID.String(), nil,
	))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if service.deletedReferenceID != referenceID.String() {
		t.Fatalf("deleted reference = %q", service.deletedReferenceID)
	}

	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete, "/api/references/not-a-uuid", nil,
	))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	service.err = sql.ErrNoRows
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete, "/api/references/"+referenceID.String(), nil,
	))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing reference status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestPaperCitationHandlerUpdateReferenceSource verifies that PATCH binds the
// partial source fields, returns the updated reference, and maps invalid JSON
// to 400 and a missing row to 404.
func TestPaperCitationHandlerUpdateReferenceSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	referenceID := uuid.New()
	service := &paperCitationHandlerStub{
		reference: types.PaperReference{
			ReferenceID:    referenceID,
			Title:          "Corrected title",
			MetadataSource: types.PaperMetadataSourceManual,
		},
	}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.PATCH("/api/references/:referenceID", NewPaperCitationHandler(service).UpdateReferenceSource)

	body, err := json.Marshal(map[string]any{
		"title":       "Corrected title",
		"doi":         "10.1000/corrected",
		"authors":     []types.PaperAuthor{},
		"publishedAt": "2024-03-02",
	})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPatch, "/api/references/"+referenceID.String(), bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if service.updatedReferenceID != referenceID.String() {
		t.Fatalf("updated reference = %q", service.updatedReferenceID)
	}
	if service.updatedSource.Title == nil || *service.updatedSource.Title != "Corrected title" ||
		service.updatedSource.DOI == nil || *service.updatedSource.DOI != "10.1000/corrected" ||
		service.updatedSource.Authors == nil || len(*service.updatedSource.Authors) != 0 ||
		service.updatedSource.PublishedAt == nil || *service.updatedSource.PublishedAt != "2024-03-02" {
		t.Fatalf("bound update = %#v", service.updatedSource)
	}
	var response struct {
		Reference types.PaperReference `json:"reference"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Reference.ReferenceID != referenceID || response.Reference.Title != "Corrected title" {
		t.Fatalf("response reference = %#v", response.Reference)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPatch, "/api/references/"+referenceID.String(), bytes.NewReader([]byte("{")),
	)
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	service.err = sql.ErrNoRows
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(
		http.MethodPatch, "/api/references/"+referenceID.String(), bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing reference status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_info_test.go.
// ---------------------------------------------------------------------------

type paperInfoControlHandlerStub struct {
	extractions   int
	catalogs      int
	references    int
	cancellations int
}

func (*paperInfoControlHandlerStub) ExtractMetadata(
	context.Context,
	types.PaperMetadataExtractCommand,
) (bool, error) {
	return false, nil
}

func (*paperInfoControlHandlerStub) PaperSourceQueryEnqueue(
	context.Context,
	types.PaperSourceQueryEnqueueCommand,
) error {
	return nil
}

func (*paperInfoControlHandlerStub) ReferenceQueryEnqueue(
	context.Context,
	types.PaperReferenceQueryEnqueueCommand,
) error {
	return nil
}

func (*paperInfoControlHandlerStub) QueryApply(
	context.Context,
	types.PaperQueryApplyCommand,
) error {
	return nil
}
func (s *paperInfoControlHandlerStub) RetryExtraction(context.Context, string) error {
	s.extractions++
	return nil
}
func (s *paperInfoControlHandlerStub) RetryCatalogResolution(context.Context, string) error {
	s.catalogs++
	return nil
}
func (s *paperInfoControlHandlerStub) RetryReferenceResolution(context.Context, string) error {
	s.references++
	return nil
}
func (s *paperInfoControlHandlerStub) Cancel(context.Context, string) error {
	s.cancellations++
	return nil
}

// TestPaperInfoHandlerExposesRetryAndCancellationControls verifies that all
// four stage retry and cancel endpoints return 202 and invoke each control once.
func TestPaperInfoHandlerExposesRetryAndCancellationControls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	documentID := uuid.New()
	controls := &paperInfoControlHandlerStub{}
	handler := NewPaperInfoHandler(controls)
	engine := gin.New()
	engine.POST("/api/paper/:documentID/info/extraction/retry", handler.RetryExtraction)
	engine.POST("/api/paper/:documentID/info/catalog/retry", handler.RetryCatalog)
	engine.POST("/api/paper/:documentID/info/reference/retry", handler.RetryReference)
	engine.POST("/api/paper/:documentID/info/cancel", handler.Cancel)

	for _, request := range []struct{ method, path string }{
		{http.MethodPost, "/api/paper/" + documentID.String() + "/info/extraction/retry"},
		{http.MethodPost, "/api/paper/" + documentID.String() + "/info/catalog/retry"},
		{http.MethodPost, "/api/paper/" + documentID.String() + "/info/reference/retry"},
		{http.MethodPost, "/api/paper/" + documentID.String() + "/info/cancel"},
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		want := http.StatusAccepted
		if recorder.Code != want {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.path, recorder.Code, recorder.Body.String())
		}
	}
	if controls.extractions != 1 || controls.catalogs != 1 || controls.references != 1 || controls.cancellations != 1 {
		t.Fatalf("control calls = extraction:%d catalog:%d reference:%d cancellation:%d", controls.extractions, controls.catalogs, controls.references, controls.cancellations)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_metadata_test.go.
// ---------------------------------------------------------------------------

type paperHandlerServiceStub struct {
	// Reference-specific operations are not exercised by these handler tests.
	interfaces.PaperService
	metadata   types.PaperMetadata
	lastUpdate types.PaperMetadataUpdate
	lastSearch types.PaperSearch
	citations  []types.PaperCitation
}

func (s *paperHandlerServiceStub) GetMetadata(
	context.Context, string,
) (*types.PaperMetadata, error) {
	metadata := s.metadata
	return &metadata, nil
}

func (s *paperHandlerServiceStub) UpdateMetadata(
	_ context.Context,
	_ string,
	update types.PaperMetadataUpdate,
) (*types.PaperMetadata, error) {
	s.lastUpdate = update
	if update.Title != nil {
		s.metadata.Title = *update.Title
	}
	metadata := s.metadata
	return &metadata, nil
}

func (s *paperHandlerServiceStub) SearchMetadata(
	_ context.Context, search types.PaperSearch,
) (types.PaperMetadataPage, error) {
	s.lastSearch = search
	return types.PaperMetadataPage{
		Items:    []types.PaperMetadata{s.metadata},
		Page:     search.Page,
		PageSize: search.PageSize,
		Total:    1,
	}, nil
}

func (*paperHandlerServiceStub) AddCitation(
	context.Context, string, types.PaperCitationInput,
) (*types.PaperCitation, error) {
	return nil, nil
}

func (*paperHandlerServiceStub) ReplaceCitations(
	context.Context, string, []types.PaperCitationInput,
) ([]types.PaperCitation, error) {
	return nil, nil
}

func (*paperHandlerServiceStub) MergeExtractedCitations(
	context.Context, string, []types.PaperCitationInput,
) ([]types.PaperCitation, error) {
	return nil, nil
}

func (*paperHandlerServiceStub) RemoveCitation(context.Context, string, string) error {
	return nil
}

func (s *paperHandlerServiceStub) ListCitations(
	context.Context, string,
) ([]types.PaperCitation, error) {
	return append([]types.PaperCitation(nil), s.citations...), nil
}
func (s *paperHandlerServiceStub) ListCitationsPage(
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

func (s *paperHandlerServiceStub) ListCitedBy(
	context.Context, string,
) ([]types.PaperCitation, error) {
	return append([]types.PaperCitation(nil), s.citations...), nil
}

func (*paperHandlerServiceStub) ResolveCitation(
	context.Context, string, string, string,
) (*types.PaperCitation, error) {
	return nil, nil
}

// TestPaperMetadataHandlerKeepsGetUpdateAndSearchBehavior verifies that GET
// returns metadata, PATCH forwards the title update, and search parses title,
// author, tag, and page filters.
func TestPaperMetadataHandlerKeepsGetUpdateAndSearchBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	documentID := uuid.New()
	service := &paperHandlerServiceStub{
		metadata: types.PaperMetadata{
			DocumentID: documentID,
			Title:      "Original title",
			Authors:    []types.PaperAuthor{},
			Tags:       []string{},
		},
	}
	handler := NewPaperMetadataHandler(service)
	engine := gin.New()
	engine.GET("/api/paper/search", handler.Search)
	engine.GET("/api/paper/:documentID", handler.Get)
	engine.PATCH("/api/paper/:documentID", handler.Update)

	getRecorder := httptest.NewRecorder()
	getRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/paper/"+documentID.String(),
		nil,
	)
	engine.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", getRecorder.Code, getRecorder.Body.String())
	}

	updateBody, err := json.Marshal(map[string]string{"title": "Updated title"})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	updateRecorder := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/paper/"+documentID.String(),
		bytes.NewReader(updateBody),
	)
	updateRequest.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(updateRecorder, updateRequest)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", updateRecorder.Code, updateRecorder.Body.String())
	}
	if service.lastUpdate.Title == nil || *service.lastUpdate.Title != "Updated title" {
		t.Fatalf("PATCH update = %#v", service.lastUpdate)
	}

	searchRecorder := httptest.NewRecorder()
	searchRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/paper/search?title=query&author=Alice&tag=ai&page=2",
		nil,
	)
	engine.ServeHTTP(searchRecorder, searchRequest)
	if searchRecorder.Code != http.StatusOK {
		t.Fatalf("search status = %d, body=%s", searchRecorder.Code, searchRecorder.Body.String())
	}
	if service.lastSearch.Title != "query" ||
		service.lastSearch.Author != "Alice" ||
		service.lastSearch.Page != 2 ||
		len(service.lastSearch.Tags) != 1 ||
		service.lastSearch.Tags[0] != "ai" {
		t.Fatalf("search request = %#v", service.lastSearch)
	}
}

// TestPaperCitationHandlerListsForwardAndReverseEdges verifies that both the
// citations and cited-by endpoints return the linked reference payload.
func TestPaperCitationHandlerListsForwardAndReverseEdges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	documentID := uuid.New()
	otherID := uuid.New()
	service := &paperHandlerServiceStub{citations: []types.PaperCitation{{
		PaperReference: types.PaperReference{
			ReferenceID:      uuid.New(),
			CitingDocumentID: documentID,
			CitedDocumentID:  &otherID,
			RawReference:     "A reference",
			Authors:          []types.PaperAuthor{},
			ResolutionStatus: types.DocumentStageSucceeded,
			MetadataSource:   types.PaperMetadataSourceExtraction,
		},
	}}}
	handler := NewPaperCitationHandler(service)
	engine := gin.New()
	engine.GET("/api/paper/:documentID/citations", handler.List)
	engine.GET("/api/paper/:documentID/cited-by", handler.ListCitedBy)

	for _, path := range []string{
		"/api/paper/" + documentID.String() + "/citations",
		"/api/paper/" + documentID.String() + "/cited-by",
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(otherID.String())) {
			t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}
