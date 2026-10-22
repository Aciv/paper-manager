package handler

import (
	"bytes"
	"context"
	"mime/multipart"
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
	lastEdits       types.ReadingNoteEdits
	lastReplaceID   string
	lastReplaceRev  int64
	lastReplaceText string
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
func (s *readingNoteServiceStub) ApplyEdits(_ context.Context, _ string, request types.ReadingNoteEdits) (types.ReadingNoteEditResult, error) {
	s.lastEdits = request
	return s.result, s.err
}
func (s *readingNoteServiceStub) ReplaceSection(_ context.Context, _, sectionID string, expectedRevision int64, markdown string) (types.ReadingNoteEditResult, error) {
	s.lastReplaceID, s.lastReplaceRev, s.lastReplaceText = sectionID, expectedRevision, markdown
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
	engine.GET("/api/document/:id/reading-note/sections/:sectionId/content", handler.ReadSectionContent)
	engine.PUT("/api/document/:id/reading-note/sections/:sectionId", handler.PutSection)
	engine.POST("/api/document/:id/reading-note/operations", handler.ApplyOperations)
	engine.POST("/api/document/:id/reading-note/edits", handler.ApplyEdits)
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

func TestNoteReadingHandlerAppliesSemanticEdits(t *testing.T) {
	id := uuid.New()
	sectionID := uuid.New()
	stub := &readingNoteServiceStub{result: types.ReadingNoteEditResult{
		DocumentID: id, Revision: 4,
		Directory:       types.ReadingNoteDirectory{SchemaVersion: 1},
		ChangedSections: []types.ReadingNoteSectionEntry{{ID: sectionID}},
	}}
	engine := readingNoteTestEngine(stub)
	body := `{"expectedRevision":3,"edits":[{"op":"insertAt","sectionId":"` + sectionID.String() + `","position":{"line":2,"column":1},"content":"new\n"},{"op":"appendText","sectionId":"` + sectionID.String() + `","content":"tail"}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/document/"+id.String()+"/reading-note/edits", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(stub.lastEdits.Edits) != 2 || stub.lastEdits.ExpectedRevision != 3 ||
		stub.lastEdits.Edits[0].Op != types.ReadingNoteEditOpInsertAt ||
		stub.lastEdits.Edits[1].Op != types.ReadingNoteEditOpAppendText {
		t.Fatalf("edit status=%d body=%s request=%#v", response.Code, response.Body, stub.lastEdits)
	}
}

func multipartMarkdownUpload(t *testing.T, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

func TestNoteReadingHandlerReadsRawSectionContent(t *testing.T) {
	id := uuid.New()
	sectionID := uuid.New()
	stub := &readingNoteServiceStub{
		note:            types.DocumentReadingNote{DocumentID: id, Revision: 2},
		sectionMarkdown: "# Section\nbody\n",
	}
	engine := readingNoteTestEngine(stub)
	request := httptest.NewRequest(http.MethodGet,
		"/api/document/"+id.String()+"/reading-note/sections/"+sectionID.String()+"/content", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "# Section\nbody\n" ||
		response.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Fatalf("section content status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestNoteReadingHandlerUploadsWholeAndSectionMarkdownFiles(t *testing.T) {
	id := uuid.New()
	sectionID := uuid.New()
	stub := &readingNoteServiceStub{note: types.DocumentReadingNote{DocumentID: id, Revision: 2}}
	engine := readingNoteTestEngine(stub)

	wholeBody, wholeType := multipartMarkdownUpload(t, "# Whole\nbody\n")
	request := httptest.NewRequest(http.MethodPut,
		"/api/document/"+id.String()+"/reading-note?expectedRevision=2", wholeBody)
	request.Header.Set("Content-Type", wholeType)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.putMarkdown != "# Whole\nbody\n" {
		t.Fatalf("whole upload status=%d body=%s markdown=%q", response.Code, response.Body, stub.putMarkdown)
	}

	sectionBody, sectionType := multipartMarkdownUpload(t, "# Section\nuploaded\n")
	request = httptest.NewRequest(http.MethodPut,
		"/api/document/"+id.String()+"/reading-note/sections/"+sectionID.String()+"?expectedRevision=3", sectionBody)
	request.Header.Set("Content-Type", sectionType)
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.lastReplaceID != sectionID.String() ||
		stub.lastReplaceRev != 3 || stub.lastReplaceText != "# Section\nuploaded\n" {
		t.Fatalf("section upload status=%d body=%s id=%s rev=%d text=%q", response.Code, response.Body,
			stub.lastReplaceID, stub.lastReplaceRev, stub.lastReplaceText)
	}
}

func TestNoteReadingHandlerRejectsOversizedUploadBeforeReading(t *testing.T) {
	id := uuid.New()
	stub := &readingNoteServiceStub{note: types.DocumentReadingNote{DocumentID: id, Revision: 1}}
	engine := readingNoteTestEngine(stub)
	request := httptest.NewRequest(http.MethodPut,
		"/api/document/"+id.String()+"/reading-note?expectedRevision=0", bytes.NewReader(nil))
	request.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	request.ContentLength = types.MaxDocumentReadingNoteBytes + 1
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || stub.putMarkdown != "" {
		t.Fatalf("oversized upload status=%d body=%s markdown=%q", response.Code, response.Body, stub.putMarkdown)
	}
}

func TestNoteReadingHandlerCreatesEmptyNoteFromRawUpload(t *testing.T) {
	id := uuid.New()
	stub := &readingNoteServiceStub{note: types.DocumentReadingNote{DocumentID: id, Revision: 1}}
	engine := readingNoteTestEngine(stub)
	request := httptest.NewRequest(http.MethodPut,
		"/api/document/"+id.String()+"/reading-note?expectedRevision=0", bytes.NewReader(nil))
	request.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.putMarkdown != "" {
		t.Fatalf("empty upload status=%d body=%s markdown=%q", response.Code, response.Body, stub.putMarkdown)
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
