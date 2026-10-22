package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestReadingNoteEditsUseSnapshotPositions(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# H\nalpha\nbeta\ngamma\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	section, err := service.ReadSection(context.Background(), id.String(), sectionID.String())
	if err != nil || section.LineCount != 4 {
		t.Fatalf("section = %#v, error=%v", section, err)
	}

	result, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
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
	})
	if err != nil || result.Revision != 2 || len(result.ChangedSections) != 1 {
		t.Fatalf("edits = %#v, error=%v", result, err)
	}
	updated, err := service.ReadSection(context.Background(), id.String(), sectionID.String())
	if err != nil {
		t.Fatal(err)
	}
	want := "# H\nalpha changed\nalpha two\nbeta2\ninserted\ngamma\n"
	if updated.Markdown != want || updated.LineCount != 6 {
		t.Fatalf("markdown = %q, lineCount=%d, want %q (6 lines)", updated.Markdown, updated.LineCount, want)
	}
}

func TestReadingNoteEditsUseCodePointColumns(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# 标题\n甲乙丙\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID
	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op:        types.ReadingNoteEditOpReplaceRange,
			SectionID: &sectionID,
			Start:     &types.ReadingNotePosition{Line: 2, Column: 2},
			End:       &types.ReadingNotePosition{Line: 2, Column: 3},
			Content:   "X",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	section, err := service.ReadSection(context.Background(), id.String(), sectionID.String())
	if err != nil || section.Markdown != "# 标题\n甲X丙\n" {
		t.Fatalf("section = %#v, error=%v", section, err)
	}
}

func TestReadingNoteEditsRejectAmbiguousText(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# H\nsame\nsame\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID

	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpReplaceText, SectionID: &sectionID, OldText: "same", NewText: "changed",
		}},
	}); !errors.Is(err, types.ErrInvalidReadingNote) || !strings.Contains(err.Error(), "matched multiple times") {
		t.Fatalf("ambiguous replace error = %v", err)
	}
	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpReplaceText, SectionID: &sectionID, OldText: "same", NewText: "changed",
			LineStart: 2, LineEnd: 2,
		}},
	}); err != nil {
		t.Fatalf("windowed replace: %v", err)
	}
	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 2,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpReplaceText, SectionID: &sectionID, OldText: "missing", NewText: "x",
		}},
	}); !errors.Is(err, types.ErrInvalidReadingNote) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing replace error = %v", err)
	}
	section, err := service.ReadSection(context.Background(), id.String(), sectionID.String())
	if err != nil || section.Markdown != "# H\nchanged\nsame\n" || section.Revision != 2 {
		t.Fatalf("section after rejected edits = %#v, error=%v", section, err)
	}
}

func TestReadingNoteEditsRejectOverlappingRanges(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# H\nalpha\nbeta\n"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID

	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{
			{
				Op: types.ReadingNoteEditOpReplaceRange, SectionID: &sectionID,
				Start:   &types.ReadingNotePosition{Line: 2, Column: 1},
				End:     &types.ReadingNotePosition{Line: 3, Column: 1},
				Content: "first",
			},
			{
				Op: types.ReadingNoteEditOpReplaceRange, SectionID: &sectionID,
				Start:   &types.ReadingNotePosition{Line: 2, Column: 2},
				End:     &types.ReadingNotePosition{Line: 2},
				Content: "second",
			},
		},
	}); !errors.Is(err, types.ErrInvalidReadingNote) || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error = %v", err)
	}
	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{
			{Op: types.ReadingNoteEditOpAppendText, SectionID: &sectionID, Content: "one"},
			{Op: types.ReadingNoteEditOpAppendText, SectionID: &sectionID, Content: "two"},
		},
	}); !errors.Is(err, types.ErrInvalidReadingNote) || !strings.Contains(err.Error(), "share start") {
		t.Fatalf("same-start error = %v", err)
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil || loaded.Markdown != "# H\nalpha\nbeta\n" || loaded.Revision != 1 {
		t.Fatalf("note after rejected ranges = %#v, error=%v", loaded, err)
	}
}

func TestReadingNoteEditsResolveStructureBeforeText(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# A\na\n## B\nb\n# C\nc"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	var rootID, childID, otherID uuid.UUID
	for _, section := range manifest.Directory.Sections {
		switch section.Title {
		case "A":
			rootID = section.ID
		case "B":
			childID = section.ID
		case "C":
			otherID = section.ID
		}
	}
	if rootID == uuid.Nil || childID == uuid.Nil || otherID == uuid.Nil {
		t.Fatalf("directory = %#v", manifest.Directory)
	}

	result, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{
			{
				Op: types.ReadingNoteEditOpReplaceText, SectionID: &childID, OldText: "b", NewText: "b changed",
			},
			{
				Op: types.ReadingNoteEditOpAddSection, ParentID: &rootID, Content: "### New\nnew body",
			},
			{Op: types.ReadingNoteEditOpDeleteSection, SectionID: &otherID},
		},
	})
	if err != nil || len(result.DeletedSectionIDs) != 1 || result.DeletedSectionIDs[0] != otherID {
		t.Fatalf("edits = %#v, error=%v", result, err)
	}
	loaded, err := service.Export(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Markdown, "b changed") ||
		!strings.Contains(loaded.Markdown, "### New\nnew body") ||
		strings.Contains(loaded.Markdown, "# C") {
		t.Fatalf("note after structural edits = %q", loaded.Markdown)
	}

	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 2,
		Edits: []types.ReadingNoteEdit{
			{Op: types.ReadingNoteEditOpDeleteSection, SectionID: &childID},
			{Op: types.ReadingNoteEditOpReplaceText, SectionID: &childID, OldText: "b changed", NewText: "lost"},
		},
	}); !errors.Is(err, types.ErrInvalidReadingNote) {
		t.Fatalf("edit deleted section error = %v", err)
	}
	if loaded, err := service.Export(context.Background(), id.String()); err != nil || loaded.Revision != 2 ||
		!strings.Contains(loaded.Markdown, "b changed") {
		t.Fatalf("note changed after rejected deleted-section edit = %#v, error=%v", loaded, err)
	}
}

func TestReadingNoteEditsReplaceWholeSection(t *testing.T) {
	id := uuid.New()
	service, _ := newReadingNoteTestService(t)
	if _, err := service.Put(context.Background(), id.String(), 0, "# H\nold body"); err != nil {
		t.Fatal(err)
	}
	manifest, err := service.GetManifest(context.Background(), id.String())
	if err != nil {
		t.Fatal(err)
	}
	sectionID := manifest.Directory.Sections[0].ID

	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpReplaceSection, SectionID: &sectionID, Content: "## Other\nnew body",
		}},
	}); !errors.Is(err, types.ErrInvalidReadingNote) {
		t.Fatalf("level-changing replaceSection error = %v", err)
	}
	result, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpReplaceSection, SectionID: &sectionID, Content: "# H2\nnew body",
		}},
	})
	if err != nil || result.Revision != 2 || result.Directory.Sections[0].Title != "H2" {
		t.Fatalf("replaceSection = %#v, error=%v", result, err)
	}
	if _, err := service.ApplyEdits(context.Background(), id.String(), types.ReadingNoteEdits{
		ExpectedRevision: 2,
		Edits: []types.ReadingNoteEdit{
			{Op: types.ReadingNoteEditOpReplaceSection, SectionID: &sectionID, Content: "# H3\nnew body"},
			{Op: types.ReadingNoteEditOpAppendText, SectionID: &sectionID, Content: "\nextra"},
		},
	}); !errors.Is(err, types.ErrInvalidReadingNote) || !strings.Contains(err.Error(), "only edit") {
		t.Fatalf("mixed replaceSection error = %v", err)
	}
	section, err := service.ReadSection(context.Background(), id.String(), sectionID.String())
	if err != nil || section.Markdown != "# H2\nnew body" || section.Revision != 2 {
		t.Fatalf("section after replaceSection = %#v, error=%v", section, err)
	}
}

func TestReadingNoteEditsRejectStaleRevision(t *testing.T) {
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
	request := types.ReadingNoteEdits{
		ExpectedRevision: 1,
		Edits: []types.ReadingNoteEdit{{
			Op: types.ReadingNoteEditOpAppendText, SectionID: &sectionID, Content: "\nmore",
		}},
	}
	if _, err := service.ApplyEdits(context.Background(), id.String(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyEdits(context.Background(), id.String(), request); !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("stale edit error = %v", err)
	}
}
