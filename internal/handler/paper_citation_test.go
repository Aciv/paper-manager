package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"paperMaster/internal/middleware"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

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
