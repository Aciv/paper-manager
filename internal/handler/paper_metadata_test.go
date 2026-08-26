package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type paperHandlerServiceStub struct {
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

func TestPaperCitationHandlerListsForwardAndReverseEdges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	documentID := uuid.New()
	otherID := uuid.New()
	service := &paperHandlerServiceStub{citations: []types.PaperCitation{{
		CitingDocumentID: documentID,
		CitedDocumentID:  otherID,
		RawReference:     "A reference",
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
