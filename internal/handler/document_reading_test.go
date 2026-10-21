package handler

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paperMaster/internal/middleware"
	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type documentReadingServiceStub struct {
	markdown  types.DocumentMarkdownContent
	structure types.DocumentReadingStructure
	blocks    []types.Block
	err       error
}

func (s *documentReadingServiceStub) OpenDocumentFile(context.Context, string) (*types.Document, io.ReadCloser, string, error) {
	return nil, nil, "", s.err
}
func (s *documentReadingServiceStub) GetDocumentBlocks(context.Context, string, int) ([]types.Block, error) {
	return s.blocks, s.err
}
func (s *documentReadingServiceStub) GetDocumentBlockImage(context.Context, string, string) (io.ReadCloser, string, error) {
	return nil, "", s.err
}
func (s *documentReadingServiceStub) GetDocumentMarkdown(context.Context, string) (types.DocumentMarkdownContent, error) {
	return s.markdown, s.err
}
func (s *documentReadingServiceStub) GetDocumentReadingStructure(context.Context, string) (types.DocumentReadingStructure, error) {
	return s.structure, s.err
}

func readingHandlerTestEngine(stub *documentReadingServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	handler := NewDocumentReadingHandler(stub)
	engine.GET("/api/document/:id/markdown", handler.Markdown)
	engine.GET("/api/document/:id/structure", handler.Structure)
	return engine
}

func TestDocumentReadingHandlerReturnsMarkdown(t *testing.T) {
	id := uuid.New()
	stub := &documentReadingServiceStub{markdown: types.DocumentMarkdownContent{
		DocumentID: id, Filename: "parsed.md", ContentType: "text/markdown", Markdown: []byte("# Paper\nbody"),
	}}
	engine := readingHandlerTestEngine(stub)
	request := httptest.NewRequest(http.MethodGet, "/api/document/"+id.String()+"/markdown", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "# Paper\nbody" ||
		!strings.Contains(response.Header().Get("Content-Disposition"), "parsed.md") {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestDocumentReadingHandlerReturnsStructureAndMapsMissing(t *testing.T) {
	id := uuid.New()
	stub := &documentReadingServiceStub{structure: types.DocumentReadingStructure{
		SchemaVersion: 6, DocumentID: id.String(),
		Outline: types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlinePDF,
			Sections: []types.DocumentOutlineSection{{Ordinal: 0, Title: "Intro", Level: 1, ParentOrdinal: -1}}},
	}}
	engine := readingHandlerTestEngine(stub)
	request := httptest.NewRequest(http.MethodGet, "/api/document/"+id.String()+"/structure", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source":"pdf_outline"`) ||
		strings.Contains(response.Body.String(), `"blocks"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	stub.err = sql.ErrNoRows
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing structure status=%d body=%s", response.Code, response.Body.String())
	}
}
