package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paperMaster/internal/middleware"
	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type documentStructureServiceStub struct {
	structure types.DocumentReadingStructure
	err       error
}

func (s *documentStructureServiceStub) GetDocumentReadingStructure(context.Context, string) (types.DocumentReadingStructure, error) {
	return s.structure, s.err
}

func TestDocumentStructureHandlerReturnsBoundedProjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	documentID := uuid.NewString()
	handler := NewDocumentStructureHandler(&documentStructureServiceStub{structure: types.DocumentReadingStructure{
		SchemaVersion: 2, DocumentID: documentID,
		Outline: types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlinePDF,
			Sections: []types.DocumentOutlineSection{{Ordinal: 0, Title: "Intro", Level: 1, ParentOrdinal: -1}}},
	}})
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.GET("/api/document/:id/structure", handler.Get)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/document/"+documentID+"/structure", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source":"pdf_outline"`) ||
		strings.Contains(response.Body.String(), `"blocks"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDocumentStructureHandlerValidatesIDAndMapsMissingManifest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		path string
		err  error
		want int
	}{
		{path: "/api/document/not-a-uuid/structure", want: http.StatusBadRequest},
		{path: "/api/document/" + uuid.NewString() + "/structure", err: sql.ErrNoRows, want: http.StatusNotFound},
	} {
		handler := NewDocumentStructureHandler(&documentStructureServiceStub{err: test.err})
		engine := gin.New()
		engine.Use(middleware.ErrorHandler())
		engine.GET("/api/document/:id/structure", handler.Get)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}
