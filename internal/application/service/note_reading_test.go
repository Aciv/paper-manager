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

func TestNoteReadingAddSectionHonorsParentAndSiblingPosition(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# A\na\n## A1\na1\n# B\nb\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil || len(manifest.Directory.Sections) != 3 {
		t.Fatalf("manifest = %#v, error=%v", manifest, err)
	}
	aID := manifest.Directory.Sections[0].ID
	a1ID := manifest.Directory.Sections[1].ID
	bID := manifest.Directory.Sections[2].ID
	result, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpAddSection, ParentID: &aID, Markdown: "## A2\na2\n"},
			{Op: types.ReadingNoteOpAddSection, ParentID: &aID, AfterID: &a1ID, Markdown: "## A1.5\na1.5\n"},
			{Op: types.ReadingNoteOpAddSection, AfterID: &bID, Markdown: "# C\nc\n"},
		},
	})
	if err != nil || result.Revision != 2 {
		t.Fatalf("operations = %#v, error=%v", result, err)
	}
	titles := make([]string, len(result.Directory.Sections))
	sections := make(map[string]types.ReadingNoteSectionEntry, len(result.Directory.Sections))
	for index, section := range result.Directory.Sections {
		titles[index] = section.Title
		sections[section.Title] = section
	}
	want := []string{"A", "A1", "A1.5", "A2", "B", "C"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("section order = %v, want %v", titles, want)
	}
	if sections["A1.5"].ParentID == nil || *sections["A1.5"].ParentID != aID ||
		sections["A2"].ParentID == nil || *sections["A2"].ParentID != aID ||
		sections["C"].ParentID != nil {
		t.Fatalf("section parents = %#v", sections)
	}

	// A bare append of a level-2 section inherits the nearest preceding lower
	// level as its parent instead of becoming a root entry.
	appended, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 2,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpAddSection, Markdown: "## A3\na3\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := appended.Directory.Sections[len(appended.Directory.Sections)-1]
	if last.Title != "A3" || last.ParentID == nil || *last.ParentID != sections["C"].ID {
		t.Fatalf("appended section = %#v, C = %#v", last, sections["C"])
	}
}

func TestNoteReadingEmptyNoteCanBeCreatedAndStarted(t *testing.T) {
	id := uuid.New()
	notes := &memoryReadingNoteRepository{notes: make(map[uuid.UUID]types.DocumentReadingNote)}
	storage, storageDirectory := readingNoteTestStorage(t)
	service := NewNoteReadingService(notes, storage)
	created, err := service.Put(context.Background(), id.String(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || len(created.Directory.Sections) != 0 || created.SizeBytes != 0 {
		t.Fatalf("created empty note = %#v", created)
	}
	exported, err := service.Export(context.Background(), id.String())
	if err != nil || exported.Markdown != "" {
		t.Fatalf("empty export = %#v, error=%v", exported, err)
	}

	started, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpAddSection, Markdown: "# First\nbody\n"},
		},
	})
	if err != nil || started.Revision != 2 || len(started.Directory.Sections) != 1 {
		t.Fatalf("started note = %#v, error=%v", started, err)
	}
	if exported, err := service.Export(context.Background(), id.String()); err != nil || !strings.Contains(exported.Markdown, "# First\nbody") {
		t.Fatalf("started export = %#v, error=%v", exported, err)
	}

	cleared, err := service.Put(context.Background(), id.String(), 2, "")
	if err != nil || cleared.Revision != 3 || len(cleared.Directory.Sections) != 0 || cleared.SizeBytes != 0 {
		t.Fatalf("cleared note = %#v, error=%v", cleared, err)
	}
	if count := countStoredReadingNotes(t, storageDirectory); count != 0 {
		t.Fatalf("section files after clearing = %d, want 0", count)
	}
}

func TestNoteReadingApplyEditsRejectsOversizedInlineText(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# A\nalpha\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	_, err = service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpReplaceText, SectionID: &sectionID,
			OldText: strings.Repeat("x", types.MaxReadingNoteInlineEditFieldBytes+1), NewText: "y",
		}},
	})
	if !errors.Is(err, types.ErrInvalidReadingNote) || !strings.Contains(err.Error(), "inline text limit") {
		t.Fatalf("oversized replaceText error = %v", err)
	}
}

func TestNoteReadingRawSectionReplaceBypassesInlineEditBudget(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# A\nalpha\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	large := "# A\n" + strings.Repeat("x", types.MaxReadingNoteInlineEditFieldBytes+1024)
	result, err := service.ReplaceSection(context.Background(), id.String(), sectionID.String(), 1, large)
	if err != nil || result.Revision != 2 {
		t.Fatalf("raw section replace = %#v, error=%v", result, err)
	}
	exported, err := service.Export(context.Background(), id.String())
	if err != nil || len(exported.Markdown) != len(large) {
		t.Fatalf("exported size = %d, want %d (error=%v)", len(exported.Markdown), len(large), err)
	}
}

func TestNoteReadingOperationsDoNotRenumberExistingOrderKeys(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# A\na\n# B\nb\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil || len(manifest.Directory.Sections) != 2 {
		t.Fatalf("manifest = %#v, error=%v", manifest, err)
	}
	aEntry, bEntry := manifest.Directory.Sections[0], manifest.Directory.Sections[1]
	if !(aEntry.Order < bEntry.Order) {
		t.Fatalf("initial orders = %v, %v", aEntry.Order, bEntry.Order)
	}

	added, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 1,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpAddSection, ParentID: &aEntry.ID, Markdown: "## A1\na1\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := func(sections []types.ReadingNoteSectionEntry) map[string]types.ReadingNoteSectionEntry {
		result := make(map[string]types.ReadingNoteSectionEntry, len(sections))
		for _, section := range sections {
			result[section.Title] = section
		}
		return result
	}
	sections := byTitle(added.Directory.Sections)
	if sections["A"].Order != aEntry.Order || sections["B"].Order != bEntry.Order {
		t.Fatalf("existing orders changed on insert: %#v", sections)
	}
	if !(sections["A1"].Order > aEntry.Order && sections["A1"].Order < bEntry.Order) {
		t.Fatalf("child order = %v, want between %v and %v", sections["A1"].Order, aEntry.Order, bEntry.Order)
	}

	a1ID := sections["A1"].ID
	deleted, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 2,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpDeleteSection, SectionID: &a1ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sections = byTitle(deleted.Directory.Sections)
	if sections["A"].Order != aEntry.Order || sections["B"].Order != bEntry.Order {
		t.Fatalf("existing orders changed on delete: %#v", sections)
	}

	after, err := service.ApplyOperations(context.Background(), id.String(), types.ReadingNoteOperations{
		ExpectedRevision: 3,
		Operations: []types.ReadingNoteOperation{
			{Op: types.ReadingNoteOpAddSection, AfterID: &aEntry.ID, Markdown: "# A2\na2\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sections = byTitle(after.Directory.Sections)
	if sections["A"].Order != aEntry.Order || sections["B"].Order != bEntry.Order ||
		!(sections["A2"].Order > aEntry.Order && sections["A2"].Order < bEntry.Order) {
		t.Fatalf("orders after next insertion = %#v", sections)
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
