package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestNoteReadingOptimisticCRUDAndCascadePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	notes := NewNoteReadingRepository(db)
	var markdownColumnCount, storageKeyColumnCount, structureColumnCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_reading_notes' AND column_name = 'markdown'`).Scan(&markdownColumnCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_reading_notes' AND column_name = 'storage_key'`).Scan(&storageKeyColumnCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_reading_notes' AND column_name = 'structure'`).Scan(&structureColumnCount).Error; err != nil {
		t.Fatal(err)
	}
	if markdownColumnCount != 0 || storageKeyColumnCount != 0 || structureColumnCount != 1 {
		t.Fatalf("reading note columns markdown=%d storage_key=%d structure=%d", markdownColumnCount, storageKeyColumnCount, structureColumnCount)
	}
	documentID := uuid.New()
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(documentID, "note.pdf", "notes/source", "")); err != nil {
		t.Fatalf("create document: %v", err)
	}
	created, err := notes.Put(ctx, readingNoteManifest(documentID, "first"), 0)
	if err != nil || created.Revision != 1 {
		t.Fatalf("create note = %#v, error=%v", created, err)
	}
	if _, err := notes.Put(ctx, readingNoteManifest(documentID, "overwrite"), 0); !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("duplicate create error = %v", err)
	}
	updated, err := notes.Put(ctx, readingNoteManifest(documentID, "updated"), 1)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update note = %#v, error=%v", updated, err)
	}
	if updated.Directory.Sections[0].StorageKey == created.Directory.Sections[0].StorageKey {
		t.Fatal("updated manifest did not replace the section key")
	}
	if _, err := notes.Put(ctx, readingNoteManifest(documentID, "stale"), 1); !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if _, err := notes.Delete(ctx, documentID, 1); !errors.Is(err, types.ErrReadingNoteConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := documents.Delete(ctx, documentID); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	if _, err := notes.Get(ctx, documentID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("note survived cascade: %v", err)
	}
}

func TestRecognitionReplacementDoesNotRewriteReadingNotePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	notes := NewNoteReadingRepository(db)
	documentID := uuid.New()
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(documentID, "note.pdf", "notes/recognition-source", "")); err != nil {
		t.Fatalf("create document: %v", err)
	}
	note, err := notes.Put(ctx, readingNoteManifest(documentID, "stable"), 0)
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	generation := startMarkdownStagePostgres(t, ctx, documents, documentID)
	if err := documents.ReplaceParsedOutput(ctx, parsedMarkdownForTest(documentID, "notes/recognized.md", 1), parsedTestBlocks(documentID, 1, 0), generation); err != nil {
		t.Fatalf("replace parsed output: %v", err)
	}
	after, err := notes.Get(ctx, documentID)
	if err != nil || after.Directory.Sections[0].StorageKey != note.Directory.Sections[0].StorageKey || after.Revision != note.Revision {
		t.Fatalf("note changed during recognition: before=%#v after=%#v err=%v", note, after, err)
	}
}

func TestSameSHAUploadKeepsExistingNoteReadingPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	notes := NewNoteReadingRepository(db)
	existingID := uuid.New()
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(existingID, "same.pdf", "notes/same-source", hash)); err != nil {
		t.Fatalf("create existing document: %v", err)
	}
	note, err := notes.Put(ctx, readingNoteManifest(existingID, "existing"), 0)
	if err != nil {
		t.Fatalf("create existing note: %v", err)
	}
	_, err = documents.Create(ctx, sourceDocumentForCanonicalTest(uuid.New(), "duplicate.pdf", "notes/duplicate-source", hash))
	if !errors.Is(err, types.ErrDuplicateFileHash) {
		t.Fatalf("same-SHA create error = %v", err)
	}
	after, err := notes.Get(ctx, existingID)
	if err != nil || after.Directory.Sections[0].StorageKey != note.Directory.Sections[0].StorageKey || after.Revision != note.Revision {
		t.Fatalf("same-SHA duplicate changed existing note: before=%#v after=%#v err=%v", note, after, err)
	}
}

func TestNewestReplacementKeepsNewestReadingNoteAndCleansOlderFilePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	notes := NewNoteReadingRepository(db)
	olderID, newestID := uuid.New(), uuid.New()
	older := sourceDocumentForCanonicalTest(olderID, "older.pdf", "notes/replace-old-source", "")
	older.DOI = "10.1000/note-replacement"
	if _, err := documents.Create(ctx, older); err != nil {
		t.Fatal(err)
	}
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(newestID, "newest.pdf", "notes/replace-new-source", "")); err != nil {
		t.Fatal(err)
	}
	olderNote, err := notes.Put(ctx, readingNoteManifest(olderID, "older-owned"), 0)
	if err != nil {
		t.Fatal(err)
	}
	newestNote, err := notes.Put(ctx, readingNoteManifest(newestID, "newest-owned"), 0)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ReplaceOlderDocument(ctx, olderID, newestID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := notes.Get(ctx, newestID)
	if err != nil || after.Directory.Sections[0].StorageKey != newestNote.Directory.Sections[0].StorageKey {
		t.Fatalf("newest note = %#v, error=%v", after, err)
	}
	foundOldNote := false
	for _, key := range keys.StorageKeys {
		foundOldNote = foundOldNote || key == olderNote.Directory.Sections[0].StorageKey
	}
	if !foundOldNote {
		t.Fatalf("older note key missing from cleanup: %#v", keys.StorageKeys)
	}
}

func readingNoteManifest(documentID uuid.UUID, suffix string) types.DocumentReadingNote {
	note := types.DocumentReadingNote{DocumentID: documentID, SizeBytes: 8}
	sectionID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(documentID.String()+suffix))
	if err := note.SetDirectory(types.ReadingNoteDirectory{SchemaVersion: 1, Sections: []types.ReadingNoteSectionEntry{{
		ID: sectionID, Order: 0, Level: 1, Title: suffix, StorageKey: "notes/" + documentID.String() + "/" + sectionID.String() + ".md",
		ContentRevision: 1, SizeBytes: 8,
	}}}); err != nil {
		panic(err)
	}
	return note
}
