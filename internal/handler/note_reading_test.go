package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	appservice "paperMaster/internal/application/service"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/middleware"
	"paperMaster/internal/types"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Consolidated from note_reading_test.go.
// ---------------------------------------------------------------------------

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

// TestNoteReadingHandlerWritesMarkdownAndExports verifies that a JSON whole-note
// PUT forwards the Markdown and that export returns raw Markdown bytes.
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

// TestNoteReadingHandlerReturnsManifestWithoutMarkdown verifies that the
// manifest includes the revision and directory but omits the full note Markdown.
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

// TestNoteReadingHandlerAcceptsMarkdownAndReadsSection verifies that a raw
// text/markdown PUT stores the body and that section reads return its content.
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

// TestNoteReadingHandlerAppliesOperationSequence verifies that primitive
// operations are decoded in order with the expected revision and forwarded.
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

// TestNoteReadingHandlerAppliesSemanticEdits verifies that semantic edits are
// decoded with their expected revision and forwarded to the service.
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

// TestNoteReadingHandlerReadsRawSectionContent verifies that the section
// content endpoint streams raw Markdown with the Markdown content type.
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

// TestNoteReadingHandlerUploadsWholeAndSectionMarkdownFiles verifies that
// multipart .md uploads replace the whole note and a single section using the
// revision fence and file content.
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

// TestNoteReadingHandlerRejectsOversizedUploadBeforeReading verifies that a
// note upload over the byte limit is rejected with 413 without calling the service.
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

// TestNoteReadingHandlerCreatesEmptyNoteFromRawUpload verifies that an empty
// raw Markdown body creates a blank note instead of an error.
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

// TestNoteReadingHandlerMapsRevisionConflict verifies that a revision conflict
// reported by the service is mapped to 409.
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

// ---------------------------------------------------------------------------
// Consolidated from note_reading_integration_test.go.
// ---------------------------------------------------------------------------

type httpReadingNoteRepository struct {
	notes map[uuid.UUID]types.DocumentReadingNote
}

func (r *httpReadingNoteRepository) Get(_ context.Context, id uuid.UUID) (types.DocumentReadingNote, error) {
	note, ok := r.notes[id]
	if !ok {
		return types.DocumentReadingNote{}, sql.ErrNoRows
	}
	return note, nil
}

func (r *httpReadingNoteRepository) Put(
	_ context.Context, candidate types.DocumentReadingNote, expectedRevision int64,
) (types.DocumentReadingNote, error) {
	current, exists := r.notes[candidate.DocumentID]
	if (!exists && expectedRevision != 0) || (exists && current.Revision != expectedRevision) {
		return types.DocumentReadingNote{}, types.ErrReadingNoteConflict
	}
	candidate.Revision = expectedRevision + 1
	r.notes[candidate.DocumentID] = candidate
	return candidate, nil
}

func (r *httpReadingNoteRepository) Delete(
	_ context.Context, id uuid.UUID, expectedRevision int64,
) (types.DocumentReadingNote, error) {
	current, exists := r.notes[id]
	if !exists || current.Revision != expectedRevision {
		return types.DocumentReadingNote{}, types.ErrReadingNoteConflict
	}
	delete(r.notes, id)
	return current, nil
}

func newHTTPReadingNoteServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	storageDirectory := t.TempDir()
	storage, err := fileservice.NewLocalStorage(storageDirectory, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	notes := appservice.NewNoteReadingService(&httpReadingNoteRepository{
		notes: make(map[uuid.UUID]types.DocumentReadingNote),
	}, storage)
	noteHandler := NewNoteReadingHandler(notes)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.PUT("/api/document/:id/reading-note", noteHandler.Put)
	engine.GET("/api/document/:id/reading-note/sections/:sectionId", noteHandler.ReadSection)
	engine.POST("/api/document/:id/reading-note/edits", noteHandler.ApplyEdits)
	engine.GET("/api/document/:id/reading-note/export", noteHandler.Export)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	return server, storageDirectory
}

func doHTTP(t *testing.T, client *http.Client, method, url string, body []byte, contentType string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, payload
}

// TestNoteReadingSemanticEditsModifyStoredSectionOverHTTP verifies end to end
// over HTTP that semantic edits rewrite the stored section and export, and that
// a stale revision is rejected without changing the note.
func TestNoteReadingSemanticEditsModifyStoredSectionOverHTTP(t *testing.T) {
	server, storageDirectory := newHTTPReadingNoteServer(t)
	client := server.Client()
	documentID := uuid.New()
	base := server.URL + "/api/document/" + documentID.String()

	createdResponse, createdBody := doHTTP(t, client, http.MethodPut, base+"/reading-note?expectedRevision=0",
		[]byte("# H\nalpha\nbeta\ngamma\n"), "text/markdown; charset=utf-8")
	if createdResponse.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createdResponse.StatusCode, createdBody)
	}
	var created types.DocumentReadingNote
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Directory.Sections) != 1 {
		t.Fatalf("created directory = %#v", created.Directory)
	}
	sectionID := created.Directory.Sections[0].ID

	edits := types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{
			{
				Op:        types.ReadingNoteEditOpReplaceRange,
				SectionID: &sectionID,
				Start:     &types.ReadingNotePosition{Line: 2, Column: 1},
				End:       &types.ReadingNotePosition{Line: 2},
				Content:   "alpha changed\nalpha two",
			},
			{
				Op:        types.ReadingNoteEditOpInsertAt,
				SectionID: &sectionID,
				Position:  &types.ReadingNotePosition{Line: 4, Column: 1},
				Content:   "inserted\n",
			},
			{
				Op:        types.ReadingNoteEditOpReplaceText,
				SectionID: &sectionID,
				OldText:   "beta",
				NewText:   "beta2",
				LineStart: 3,
				LineEnd:   3,
			},
		},
	}
	payload, err := json.Marshal(edits)
	if err != nil {
		t.Fatal(err)
	}
	editResponse, editBody := doHTTP(t, client, http.MethodPost, base+"/reading-note/edits", payload, "application/json")
	if editResponse.StatusCode != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", editResponse.StatusCode, editBody)
	}
	var editResult types.ReadingNoteEditResult
	if err := json.Unmarshal(editBody, &editResult); err != nil {
		t.Fatal(err)
	}
	if editResult.Revision != 2 || len(editResult.ChangedSections) != 1 {
		t.Fatalf("edit result = %#v", editResult)
	}

	sectionResponse, sectionBody := doHTTP(t, client, http.MethodGet,
		base+"/reading-note/sections/"+sectionID.String(), nil, "")
	if sectionResponse.StatusCode != http.StatusOK {
		t.Fatalf("section status=%d body=%s", sectionResponse.StatusCode, sectionBody)
	}
	var section types.ReadingNoteSection
	if err := json.Unmarshal(sectionBody, &section); err != nil {
		t.Fatal(err)
	}
	want := "# H\nalpha changed\nalpha two\nbeta2\ninserted\ngamma\n"
	if section.Markdown != want || section.LineCount != 6 || section.Revision != 2 {
		t.Fatalf("section = %#v, want markdown %q", section, want)
	}
	diskContent, err := os.ReadFile(filepath.Join(storageDirectory, "notes", documentID.String(), sectionID.String()+".md"))
	if err != nil {
		t.Fatalf("read stored section: %v", err)
	}
	if string(diskContent) != want {
		t.Fatalf("stored section = %q, want %q", diskContent, want)
	}

	exportResponse, exportBody := doHTTP(t, client, http.MethodGet, base+"/reading-note/export", nil, "")
	if exportResponse.StatusCode != http.StatusOK || string(exportBody) != want {
		t.Fatalf("export status=%d body=%q, want %q", exportResponse.StatusCode, exportBody, want)
	}

	stale := edits
	stale.Edits = []types.ReadingNoteEdit{{
		Op: types.ReadingNoteEditOpAppendText, SectionID: &sectionID, Content: "\nstale",
	}}
	payload, err = json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	staleResponse, staleBody := doHTTP(t, client, http.MethodPost, base+"/reading-note/edits", payload, "application/json")
	if staleResponse.StatusCode != http.StatusConflict {
		t.Fatalf("stale edit status=%d body=%s", staleResponse.StatusCode, staleBody)
	}
	_, finalBody := doHTTP(t, client, http.MethodGet, base+"/reading-note/export", nil, "")
	if string(finalBody) != want {
		t.Fatalf("note changed after stale edit: %q", finalBody)
	}
}
