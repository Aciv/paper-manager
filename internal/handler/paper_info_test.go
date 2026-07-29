package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type paperInfoControlHandlerStub struct {
	status      types.PaperInfoStatus
	extractions int
	catalogs    int
}

func (*paperInfoControlHandlerStub) ExtractMetadata(
	context.Context,
	types.PaperMetadataExtractCommand,
) (bool, error) {
	return false, nil
}

func (*paperInfoControlHandlerStub) ResolveMetadata(
	context.Context,
	types.PaperCatalogResolveCommand,
) error {
	return nil
}

func (s *paperInfoControlHandlerStub) GetStatus(context.Context, string) (types.PaperInfoStatus, error) {
	return s.status, nil
}
func (s *paperInfoControlHandlerStub) RetryExtraction(context.Context, string) (types.PaperInfoStatus, error) {
	s.extractions++
	s.status.Extraction.State = types.PaperInfoStageQueued
	return s.status, nil
}
func (s *paperInfoControlHandlerStub) RetryCatalogResolution(context.Context, string) (types.PaperInfoStatus, error) {
	s.catalogs++
	s.status.Catalog.State = types.PaperInfoStageQueued
	return s.status, nil
}

func TestPaperInfoHandlerExposesStatusAndRetryControls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	documentID := uuid.New()
	controls := &paperInfoControlHandlerStub{status: types.PaperInfoStatus{
		DocumentID: documentID,
		Extraction: types.PaperInfoStageStatus{State: types.PaperInfoStageIdle},
		Catalog:    types.PaperInfoStageStatus{State: types.PaperInfoStageIdle},
	}}
	handler := NewPaperInfoHandler(controls)
	engine := gin.New()
	engine.GET("/api/paper/:documentID/info", handler.Status)
	engine.POST("/api/paper/:documentID/info/extraction/retry", handler.RetryExtraction)
	engine.POST("/api/paper/:documentID/info/catalog/retry", handler.RetryCatalog)

	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/paper/" + documentID.String() + "/info"},
		{http.MethodPost, "/api/paper/" + documentID.String() + "/info/extraction/retry"},
		{http.MethodPost, "/api/paper/" + documentID.String() + "/info/catalog/retry"},
	} {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, nil))
		want := http.StatusOK
		if request.method == http.MethodPost {
			want = http.StatusAccepted
		}
		if recorder.Code != want {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.path, recorder.Code, recorder.Body.String())
		}
	}
	if controls.extractions != 1 || controls.catalogs != 1 {
		t.Fatalf("retry calls = extraction:%d catalog:%d", controls.extractions, controls.catalogs)
	}
}
