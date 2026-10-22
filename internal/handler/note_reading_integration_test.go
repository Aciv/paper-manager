package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	appservice "paperMaster/internal/application/service"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/middleware"
	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

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
