package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"paperMaster/internal/middleware"
	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type readingNoteServiceStub struct {
	note            types.DocumentReadingNote
	export          types.ReadingNoteExport
	sectionMarkdown string
	putMarkdown     string
	result          types.ReadingNoteEditResult
	err             error
	lastOps         types.ReadingNoteOperations
}

func (s *readingNoteServiceStub) GetManifest(context.Context, string) (types.DocumentReadingNote, error) {
	return s.note, s.err
}
func (s *readingNoteServiceStub) ReadSection(context.Context, string, string) (types.ReadingNoteSection, error) {
	return types.ReadingNoteSection{DocumentID: s.note.DocumentID, Revision: s.note.Revision,
		Section: types.ReadingNoteSectionEntry{ID: uuid.New(), Title: "Summary"}, Markdown: s.sectionMarkdown}, s.err
}
func (s *readingNoteServiceStub) Export(context.Context, string) (types.ReadingNoteExport, error) {
	return s.export, s.err
}
func (s *readingNoteServiceStub) ApplyOperations(_ context.Context, _ string, request types.ReadingNoteOperations) (types.ReadingNoteEditResult, error) {
	s.lastOps = request
	return s.result, s.err
}
func (s *readingNoteServiceStub) Put(_ context.Context, _ string, _ int64, markdown string) (types.DocumentReadingNote, error) {
	s.putMarkdown = markdown
	s.sectionMarkdown = markdown
	return s.note, s.err
}
func (s *readingNoteServiceStub) Delete(context.Context, string, int64) error { return s.err }

func readingNoteTestEngine(stub *readingNoteServiceStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	handler := NewNoteReadingHandler(stub)
	engine.PUT("/api/document/:id/reading-note", handler.Put)
	engine.GET("/api/document/:id/reading-note", handler.Get)
	engine.GET("/api/document/:id/reading-note/sections/:sectionId", handler.ReadSection)
	engine.POST("/api/document/:id/reading-note/operations", handler.ApplyOperations)
	engine.GET("/api/document/:id/reading-note/export", handler.Export)
	return engine
}

func TestNoteReadingHandlerWritesMarkdownAndExports(t *testing.T) {
	id := uuid.New()
	stub := &readingNoteServiceStub{note: types.DocumentReadingNote{DocumentID: id, Revision: 2}}
	engine := readingNoteTestEngine(stub)

	request := httptest.NewRequest(http.MethodPut, "/api/document/"+id.String()+"/reading-note", bytes.NewBufferString(`{"markdown":"# Updated","expectedRevision":2}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.putMarkdown != "# Updated" {
		t.Fatalf("PUT status=%d body=%s markdown=%q", response.Code, response.Body, stub.putMarkdown)
	}

	stub.export = types.ReadingNoteExport{DocumentID: id, Revision: 2, Markdown: "# Export"}
	request = httptest.NewRequest(http.MethodGet, "/api/document/"+id.String()+"/reading-note/export", nil)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "# Export" || response.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Fatalf("export status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestNoteReadingHandlerReturnsManifestWithoutMarkdown(t *testing.T) {
	id := uuid.New()
	stub := &readingNoteServiceStub{note: types.DocumentReadingNote{DocumentID: id, Revision: 3,
		Directory: types.ReadingNoteDirectory{SchemaVersion: 1}}}
	engine := readingNoteTestEngine(stub)
	request := httptest.NewRequest(http.MethodGet, "/api/document/"+id.String()+"/reading-note", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"revision":3`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"markdown"`)) {
		t.Fatalf("manifest status=%d body=%s", response.Code, response.Body)
	}
}

func TestNoteReadingHandlerAcceptsMarkdownAndReadsSection(t *testing.T) {
	id := uuid.New()
	stub := &readingNoteServiceStub{note: types.DocumentReadingNote{DocumentID: id, Revision: 1}}
	engine := readingNoteTestEngine(stub)
	request := httptest.NewRequest(http.MethodPut, "/api/document/"+id.String()+"/reading-note?expectedRevision=1", bytes.NewBufferString("# Updated\nbody"))
	request.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.putMarkdown != "# Updated\nbody" {
		t.Fatalf("Markdown PUT status=%d body=%s markdown=%q", response.Code, response.Body, stub.putMarkdown)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/document/"+id.String()+"/reading-note/sections/"+uuid.NewString(), nil)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("# Updated")) {
		t.Fatalf("section status=%d body=%s", response.Code, response.Body)
	}
}

func TestNoteReadingHandlerAppliesOperationSequence(t *testing.T) {
	id := uuid.New()
	sectionID := uuid.New()
	stub := &readingNoteServiceStub{result: types.ReadingNoteEditResult{
		DocumentID: id, Revision: 3,
		Directory:       types.ReadingNoteDirectory{SchemaVersion: 1},
		ChangedSections: []types.ReadingNoteSectionEntry{{ID: sectionID}},
	}}
	engine := readingNoteTestEngine(stub)
	body := `{"expectedRevision":2,"operations":[{"op":"deleteText","sectionId":"` + sectionID.String() + `","offset":3,"length":2},{"op":"insertText","sectionId":"` + sectionID.String() + `","offset":3,"text":"new"}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/document/"+id.String()+"/reading-note/operations", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(stub.lastOps.Operations) != 2 || stub.lastOps.ExpectedRevision != 2 ||
		stub.lastOps.Operations[0].Op != types.ReadingNoteOpDeleteText ||
		stub.lastOps.Operations[1].Op != types.ReadingNoteOpInsertText {
		t.Fatalf("operation status=%d body=%s request=%#v", response.Code, response.Body, stub.lastOps)
	}
}

func TestNoteReadingHandlerMapsRevisionConflict(t *testing.T) {
	id := uuid.New()
	engine := readingNoteTestEngine(&readingNoteServiceStub{err: types.ErrReadingNoteConflict})
	request := httptest.NewRequest(http.MethodPut, "/api/document/"+id.String()+"/reading-note", bytes.NewBufferString(`{"markdown":"new","expectedRevision":1}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}
