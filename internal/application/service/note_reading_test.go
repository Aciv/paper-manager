package service

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type memoryReadingNoteRepository struct {
	notes map[uuid.UUID]types.DocumentReadingNote
}

func (r *memoryReadingNoteRepository) Get(_ context.Context, id uuid.UUID) (types.DocumentReadingNote, error) {
	note, ok := r.notes[id]
	if !ok {
		return types.DocumentReadingNote{}, sql.ErrNoRows
	}
	return note, nil
}
func (r *memoryReadingNoteRepository) Put(_ context.Context, candidate types.DocumentReadingNote, expected int64) (types.DocumentReadingNote, error) {
	id := candidate.DocumentID
	current, exists := r.notes[id]
	if (!exists && expected != 0) || (exists && current.Revision != expected) {
		return types.DocumentReadingNote{}, types.ErrReadingNoteConflict
	}
	candidate.Revision = expected + 1
	r.notes[id] = candidate
	return candidate, nil
}
func (r *memoryReadingNoteRepository) Delete(_ context.Context, id uuid.UUID, expected int64) (types.DocumentReadingNote, error) {
	current, exists := r.notes[id]
	if !exists || current.Revision != expected {
		return types.DocumentReadingNote{}, types.ErrReadingNoteConflict
	}
	delete(r.notes, id)
	return current, nil
}

func readingNoteTestStorage(t *testing.T) (*fileservice.LocalStorage, string) {
	t.Helper()
	directory := t.TempDir()
	storage, err := fileservice.NewLocalStorage(directory, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	return storage, directory
}

func countStoredReadingNotes(t *testing.T, directory string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(directory, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && filepath.Ext(entry.Name()) == ".md" {
			count++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func newReadingNoteTestService(t *testing.T) (interfaces.NoteReadingService, *memoryReadingNoteRepository) {
	t.Helper()
	notes := &memoryReadingNoteRepository{notes: make(map[uuid.UUID]types.DocumentReadingNote)}
	storage, _ := readingNoteTestStorage(t)
	return NewNoteReadingService(notes, storage), notes
}

func TestReadingNoteSectionKeysArePrivateAndDocumentScoped(t *testing.T) {
	documentID, sectionID := uuid.New(), uuid.New()
	document, section, err := parseReadingNoteSectionKey(noteSectionStorageKey(documentID, sectionID))
	if err != nil || document != documentID || section != sectionID {
		t.Fatalf("valid key = %s/%s, error=%v", document, section, err)
	}
	for _, key := range []string{
		"notes/outside.md",
		"notes/" + documentID.String() + "/" + sectionID.String() + ".txt",
		"notes/not-a-uuid/" + sectionID.String() + ".md",
		"notes/" + documentID.String() + "/not-a-uuid.md",
		"../outside.md",
	} {
		if _, _, err := parseReadingNoteSectionKey(key); !errors.Is(err, types.ErrInvalidReadingNote) {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
}

func TestNoteReadingServiceUsesOptimisticRevision(t *testing.T) {
	id := uuid.New()
	notes := &memoryReadingNoteRepository{notes: make(map[uuid.UUID]types.DocumentReadingNote)}
	storage, storageDirectory := readingNoteTestStorage(t)
	service := NewNoteReadingService(notes, storage)
	created, err := service.Put(context.Background(), id.String(), 0, "# First")
	if err != nil || created.Revision != 1 {
		t.Fatalf("create = %#v, error=%v", created, err)
	}
	if len(created.Directory.Sections) != 1 {
		t.Fatalf("created directory = %#v", created.Directory)
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil || loaded.Markdown != "# First" {
		t.Fatalf("exported note = %#v, error=%v", loaded, err)
	}
	if _, err := service.Put(context.Background(), id.String(), 0, "lost update"); !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("duplicate create error = %v", err)
	}
	if count := countStoredReadingNotes(t, storageDirectory); count != 1 {
		t.Fatalf("files after conflict = %d, want 1", count)
	}
	updated, err := service.Put(context.Background(), id.String(), 1, "# Second")
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update = %#v, error=%v", updated, err)
	}
	if count := countStoredReadingNotes(t, storageDirectory); count != 1 {
		t.Fatalf("files after replacement = %d, want 1", count)
	}
	if err := service.Delete(context.Background(), id.String(), 1); !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := service.Delete(context.Background(), id.String(), 2); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if count := countStoredReadingNotes(t, storageDirectory); count != 0 {
		t.Fatalf("files after delete = %d, want 0", count)
	}
}

func TestNoteReadingOperationsApplyAsOneSequence(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	markdown := "preface\n# 摘要\nsummary\n## 证据\nproof\n# Limits\nlimits"
	if _, err := service.Put(context.Background(), id.String(), 0, markdown); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil || len(manifest.Directory.Sections) != 4 {
		t.Fatalf("manifest = %#v, error=%v", manifest, err)
	}
	preamble, err := service.ReadSection(context.Background(), id.String(), manifest.Directory.Sections[0].ID.String())
	if err != nil || preamble.Markdown != "preface\n" {
		t.Fatalf("preamble = %#v, error=%v", preamble, err)
	}
	summaryID := manifest.Directory.Sections[1].ID
	evidenceID := manifest.Directory.Sections[2].ID
	limitsID := manifest.Directory.Sections[3].ID
	evidenceHeadingRunes := utf8.RuneCountInString("## 证据\n")
	result, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations: []types.ReadingNoteOperation{
			// Replace "proof" with "evidence changed" as delete + insert.
			{Op: types.ReadingNoteOpDeleteText, SectionID: &evidenceID, Offset: evidenceHeadingRunes, Length: utf8.RuneCountInString("proof")},
			{Op: types.ReadingNoteOpInsertText, SectionID: &evidenceID, Offset: evidenceHeadingRunes, Text: "evidence changed"},
			{Op: types.ReadingNoteOpAddSection, ParentID: &summaryID, Markdown: "## New comparison\nbody"},
			{Op: types.ReadingNoteOpDeleteSection, SectionID: &limitsID},
		},
	})
	if err != nil || result.Revision != 2 {
		t.Fatalf("operations = %#v, error=%v", result, err)
	}
	if len(result.ChangedSections) != 2 || len(result.DeletedSectionIDs) != 1 || result.DeletedSectionIDs[0] != limitsID {
		t.Fatalf("operation result = %#v", result)
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Markdown, "evidence changed") ||
		!strings.Contains(loaded.Markdown, "## New comparison\nbody") ||
		strings.Contains(loaded.Markdown, "limits") {
		t.Fatalf("loaded after operations = %q", loaded.Markdown)
	}
	if _, err := service.ReadSection(context.Background(), id.String(), limitsID.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted section read error = %v", err)
	}
	if len(result.Directory.Sections) != 4 || result.Directory.Sections[2].ID != evidenceID {
		t.Fatalf("directory after operations = %#v", result.Directory)
	}
}

func TestReadingNoteOperationsUseCodePointOffsetsSequentially(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	content := "# 标题\n甲乙丙\n"
	if _, err := service.Put(context.Background(), id.String(), 0, content); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	headingRunes := utf8.RuneCountInString("# 标题\n")
	result, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpInsertText, SectionID: &sectionID, Offset: headingRunes, Text: "X"},
			{Op: types.ReadingNoteOpDeleteText, SectionID: &sectionID, Offset: headingRunes + 1, Length: 2},
		},
	})
	if err != nil || result.Revision != 2 {
		t.Fatalf("codepoint operations = %#v, error=%v", result, err)
	}
	section, err := service.ReadSection(context.Background(), id.String(), sectionID.String())
	if err != nil || section.Markdown != "# 标题\nX丙\n" {
		t.Fatalf("section after codepoint operations = %#v, error=%v", section, err)
	}
}

func TestReadingNoteOperationsRejectInvalidRangesAndFinalHeadings(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# H\nbody"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	for _, operations := range [][]types.ReadingNoteOperation{
		{{Op: types.ReadingNoteOpInsertText, SectionID: &sectionID, Offset: 999, Text: "x"}},
		{{Op: types.ReadingNoteOpDeleteText, SectionID: &sectionID, Offset: 0, Length: 999}},
		{{Op: types.ReadingNoteOpInsertText, SectionID: &sectionID, Offset: utf8.RuneCountInString("# H\nbody"), Text: "\n## other\nx"}},
	} {
		_, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
			ExpectedRevision: 1, Operations: operations,
		})
		if !errors.Is(err, types.ErrInvalidReadingNote) {
			t.Fatalf("operations %#v error = %v", operations, err)
		}
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil || loaded.Markdown != "# H\nbody" || loaded.Revision != 1 {
		t.Fatalf("note changed after rejected operations = %#v, error=%v", loaded, err)
	}
}

func TestReadingNoteOperationsRejectStaleRevision(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# H\nx\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	headingRunes := utf8.RuneCountInString("# H\n")
	if _, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations:       []types.ReadingNoteOperation{{Op: types.ReadingNoteOpInsertText, SectionID: &sectionID, Offset: headingRunes, Text: "first "}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations:       []types.ReadingNoteOperation{{Op: types.ReadingNoteOpInsertText, SectionID: &sectionID, Offset: headingRunes, Text: "stale "}},
	})
	if !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
}

func TestReadingNoteOperationsDeletePreambleWithoutDeletingHeadings(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "preface\n# H\nbody"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	preambleID := manifest.Directory.Sections[0].ID
	result, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations:       []types.ReadingNoteOperation{{Op: types.ReadingNoteOpDeleteSection, SectionID: &preambleID}},
	})
	if err != nil || result.Revision != 2 || len(result.Directory.Sections) != 1 {
		t.Fatalf("delete preamble = %#v, error=%v", result, err)
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil || loaded.Markdown != "# H\nbody" {
		t.Fatalf("note after preamble delete = %#v, error=%v", loaded, err)
	}
}

func TestReadingNoteOperationsDeleteSubtree(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# A\na\n## B\nb\n# C\nc"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	rootID := manifest.Directory.Sections[0].ID
	result, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations:       []types.ReadingNoteOperation{{Op: types.ReadingNoteOpDeleteSection, SectionID: &rootID}},
	})
	if err != nil || len(result.Directory.Sections) != 1 || len(result.DeletedSectionIDs) != 2 {
		t.Fatalf("delete subtree = %#v, error=%v", result, err)
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil || loaded.Markdown != "# C\nc" {
		t.Fatalf("note after subtree delete = %#v, error=%v", loaded, err)
	}
}
