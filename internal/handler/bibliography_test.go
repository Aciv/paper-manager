package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paperMaster/internal/middleware"
	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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
