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
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type documentReadingServiceStub struct {
	interfaces.DocumentReadingService
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
func (s *documentReadingServiceStub) RenderPage(
	context.Context, string, int, *types.NormalizedPDFRect, int,
) (*types.RenderedPDFPage, error) {
	return &types.RenderedPDFPage{PNG: []byte("png"), ContentType: "image/png", Width: 1, Height: 1}, s.err
}

func readingHandlerTestEngine(stub *documentReadingServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	handler := NewDocumentReadingHandler(stub)
	engine.GET("/api/document/:id/markdown", handler.Markdown)
	engine.GET("/api/document/:id/structure", handler.Structure)
	engine.GET("/api/document/:id/pages/:page/image", handler.PageImage)
	return engine
}

// TestDocumentReadingHandlerRendersPageImage verifies that a page image request
// with dpi and bbox query parameters returns the rendered PNG and content type.
func TestDocumentReadingHandlerRendersPageImage(t *testing.T) {
	id := uuid.New()
	stub := &documentReadingServiceStub{}
	engine := readingHandlerTestEngine(stub)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/document/"+id.String()+"/pages/2/image?dpi=144&bbox=0.1,0.2,0.8,0.9",
		nil,
	)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if response.Body.String() != "png" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

// TestDocumentReadingHandlerRejectsInvalidPageBBox verifies that a malformed
// bbox query value is rejected with 400.
func TestDocumentReadingHandlerRejectsInvalidPageBBox(t *testing.T) {
	id := uuid.New()
	engine := readingHandlerTestEngine(&documentReadingServiceStub{})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/document/"+id.String()+"/pages/1/image?bbox=0.2,0.1",
		nil,
	)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

// TestDocumentReadingHandlerReturnsMarkdown verifies that the Markdown endpoint
// returns the parsed bytes with a filename-bearing Content-Disposition.
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

// TestDocumentReadingHandlerReturnsStructureAndMapsMissing verifies that the
// structure endpoint returns the outline without blocks and maps a missing
// document to 404.
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
