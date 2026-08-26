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

func (*paperInfoControlHandlerStub) ResolveMetadata(
	context.Context,
	types.PaperCatalogResolveCommand,
) error {
	return nil
}

func (*paperInfoControlHandlerStub) DispatchReferences(
	context.Context,
	types.PaperReferenceDispatchCommand,
) error {
	return nil
}
func (*paperInfoControlHandlerStub) DispatchReferenceDownloads(context.Context, types.PaperReferenceDownloadDispatchPayload) error {
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
func (*paperInfoControlHandlerStub) StartReferenceDownloads(context.Context, string) error {
	return nil
}
func (s *paperInfoControlHandlerStub) Cancel(context.Context, string) error {
	s.cancellations++
	return nil
}
func (*paperInfoControlHandlerStub) CancelReferenceDownloads(context.Context, string) error {
	return nil
}

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
