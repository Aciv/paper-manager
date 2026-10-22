package types

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidateReadingNoteMarkdownBoundsUTF8AndBytes(t *testing.T) {
	if err := ValidateReadingNoteMarkdown("# 注释"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadingNoteMarkdown(""); err != nil {
		t.Fatalf("empty Markdown should be allowed for a blank note: %v", err)
	}
	if err := ValidateReadingNoteMarkdown(string([]byte{0xff})); !errors.Is(err, ErrInvalidReadingNote) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	tooLarge := make([]byte, MaxDocumentReadingNoteBytes+1)
	if err := ValidateReadingNoteMarkdown(string(tooLarge)); !errors.Is(err, ErrInvalidReadingNote) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestEmptyReadingNoteDirectorySerializesSectionsAsArray(t *testing.T) {
	note := DocumentReadingNote{DocumentID: uuid.New()}
	if err := note.SetDirectory(ReadingNoteDirectory{SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if note.Directory.Sections == nil {
		t.Fatal("in-memory directory has nil sections")
	}
	if !strings.Contains(string(note.Structure), `"sections":[]`) {
		t.Fatalf("persisted directory = %s", note.Structure)
	}
	persisted := DocumentReadingNote{DocumentID: note.DocumentID, Structure: note.Structure}
	if err := persisted.ParseDirectory(); err != nil {
		t.Fatal(err)
	}
	if persisted.Directory.Sections == nil {
		t.Fatal("parsed directory has nil sections")
	}
}

func TestReadingNoteDirectoryRequiresDocumentScopedSectionPaths(t *testing.T) {
	documentID, sectionID := uuid.New(), uuid.New()
	note := DocumentReadingNote{DocumentID: documentID, SizeBytes: 8}
	directory := ReadingNoteDirectory{SchemaVersion: 1, Sections: []ReadingNoteSectionEntry{{
		ID: sectionID, Order: 0, Level: 1, Title: "Summary", StorageKey: "notes/" + documentID.String() + "/" + sectionID.String() + ".md",
		ContentRevision: 1, SizeBytes: 8,
	}}}
	if err := note.SetDirectory(directory); err != nil {
		t.Fatal(err)
	}
	directory.Sections[0].StorageKey = "notes/" + uuid.NewString() + "/" + sectionID.String() + ".md"
	if err := note.SetDirectory(directory); !errors.Is(err, ErrInvalidReadingNote) {
		t.Fatalf("cross-document key error=%v", err)
	}
}
