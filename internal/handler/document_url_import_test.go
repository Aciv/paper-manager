package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/handler"
	"paperMaster/internal/middleware"
	"paperMaster/internal/router"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
)

type httpURLImports struct {
	interfaces.URLImportService
	commands  []types.DocumentURLImportCommand
	bases     []string
	requestID string
	job       types.URLImportTask
	err       error
}

func (s *httpURLImports) EnqueueURLsToBases(_ context.Context, commands []types.DocumentURLImportCommand,
	bases []string, requestID string) (types.URLImportTask, error) {
	s.commands, s.bases, s.requestID = commands, bases, requestID
	return s.job, s.err
}

func (s *httpURLImports) GetURLImportTask(_ context.Context, id string) (types.URLImportTask, error) {
	if id != s.job.ID {
		return types.URLImportTask{}, types.ErrTaskNotFound
	}
	return s.job, s.err
}

func TestHTTPURLImportQueuesWithoutDocumentWorkAndReadsProgress(t *testing.T) {
	imports := &httpURLImports{job: types.URLImportTask{ID: "task-fixture", State: "queued", Total: 2}}
	h := handler.NewDocumentHandlerWithURLImports(config.Config{}, nil, imports)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	router.ServerFile(engine, h, nil)
	body := []byte(`{"urls":[{"url":"https://example.org/1"},{"url":"https://example.org/2","doi":"10.1000/2"}],"baseIds":["base-a","base-b"],"requestId":"stable-id"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/document/url-imports", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	if response.Code != http.StatusAccepted || len(imports.commands) != 2 || len(imports.bases) != 2 || imports.requestID != "stable-id" {
		t.Fatalf("queue route failed: %d %s", response.Code, response.Body)
	}
	imports.job.State, imports.job.Completed = "partial", 2
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/document/url-imports/task-fixture", nil))
	var job types.URLImportTask
	_ = json.Unmarshal(response.Body.Bytes(), &job)
	if response.Code != http.StatusOK || job.State != "partial" || job.Completed != 2 {
		t.Fatalf("progress missing: %s", response.Body)
	}
	for _, test := range []struct {
		err    error
		status int
	}{
		{types.ErrTaskNotFound, 404}, {types.ErrTaskInspectorUnavailable, 503},
		{service.ErrInvalidURLImport, 400}, {service.ErrURLImportRequestConflict, 409},
	} {
		imports.err = test.err
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/document/url-imports/task-fixture", nil))
		if response.Code != test.status {
			t.Fatalf("incorrect safe error: %d %s", response.Code, response.Body)
		}
	}
}
