package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDocumentSourceLookupModePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDocumentRepository(db)
	for _, mode := range []types.SourceLookupMode{"", types.SourceLookupAuto, types.SourceLookupSkip} {
		id := uuid.New()
		created, err := repo.Create(ctx, types.Document{
			ID: id, OriginalFilename: "import.pdf", ContentType: "application/pdf",
			SizeBytes: 1, StorageKey: "import/" + id.String(), SourceLookupMode: mode,
		})
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := mode.Normalized()
		loaded, err := repo.Get(ctx, id)
		if err != nil || created.SourceLookupMode != expected || loaded.SourceLookupMode != expected {
			t.Fatalf("created=%q, loaded=%q, error=%v", created.SourceLookupMode, loaded.SourceLookupMode, err)
		}
		rows, err := repo.GetMany(ctx, []uuid.UUID{id})
		if err != nil || len(rows) != 1 || rows[0].SourceLookupMode != expected {
			t.Fatalf("batch read = %#v, %v", rows, err)
		}
	}
	invalidID := uuid.New()
	_, err := repo.Create(ctx, types.Document{
		ID: invalidID, OriginalFilename: "bad.pdf", ContentType: "application/pdf",
		SizeBytes: 1, StorageKey: invalidID.String(), SourceLookupMode: "manual",
	})
	if !errors.Is(err, types.ErrInvalidDocumentImport) {
		t.Fatalf("invalid mode error = %v", err)
	}
	var count int64
	if err := db.Model(&types.Document{}).Where("id = ?", invalidID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid import created %d rows: %v", count, err)
	}
}

func TestImportMetadataFillMissingConcurrentPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	id := createLargeBlockTestDocument(t, ctx, NewDocumentRepository(db))
	repo := NewPaperMetadataRepository(db)
	manual := types.PaperMetadataSourceManual
	empty := ""
	if _, err := repo.Upsert(ctx, id, types.PaperMetadataUpdate{
		Abstract: &empty, MetadataSource: &manual,
	}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	errors := make(chan error, 16)
	for index := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			title := fmt.Sprintf("candidate-%d", index)
			abstract := "Must not replace a manual clear"
			_, err := repo.Upsert(ctx, id, types.PaperMetadataUpdate{
				Title: &title, Abstract: &abstract, MetadataSource: &manual, FillMissingOnly: true,
			})
			errors <- err
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := repo.GetByDocumentID(ctx, id)
	if err != nil || before.Title == "" || before.Abstract != "" {
		t.Fatalf("concurrent fill = %#v, %v", before, err)
	}
	otherTitle, venue := "Must not overwrite", "Added venue"
	if _, err := repo.Upsert(ctx, id, types.PaperMetadataUpdate{
		Title: &otherTitle, Venue: &venue, MetadataSource: &manual, FillMissingOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.GetByDocumentID(ctx, id)
	if after.Title != before.Title || after.Abstract != "" || after.Venue != venue ||
		after.FieldSources["abstract"] != manual {
		t.Fatalf("fill changed existing/manual values or failed to add missing venue: %#v", after)
	}
}

func TestDocumentSourceLookupModeMigrationPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	// Recreate version 32 inside this test's isolated schema, with a preexisting
	// document. Never downgrade or modify a developer's application database.
	if err := db.Exec(`ALTER TABLE documents DROP COLUMN source_lookup_mode;
		DELETE FROM schema_migrations WHERE version = 33`).Error; err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key)
		VALUES (?, 'existing.pdf', 'application/pdf', 1, ?)`, id, id.String()).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := NewDocumentRepository(db).Get(ctx, id)
	if err != nil || loaded.SourceLookupMode != types.SourceLookupAuto || loaded.OriginalFilename != "existing.pdf" {
		t.Fatalf("migrated document = %#v, %v", loaded, err)
	}
	var count int64
	if err := db.Table("schema_migrations").Where("version = 33").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("migration record count = %d, %v", count, err)
	}
	for _, value := range []any{"", "manual", nil} {
		if err := db.Exec("UPDATE documents SET source_lookup_mode = ? WHERE id = ?", value, id).Error; err == nil {
			t.Fatalf("database accepted mode %#v", value)
		}
	}
	for _, value := range []string{"skip", "auto"} {
		if err := db.Exec("UPDATE documents SET source_lookup_mode = ? WHERE id = ?", value, id).Error; err != nil {
			t.Fatalf("database rejected mode %q: %v", value, err)
		}
	}
}
