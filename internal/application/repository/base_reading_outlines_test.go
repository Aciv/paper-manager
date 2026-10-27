package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

type outlineSelectCounter struct {
	gormlog.Interface
	selects atomic.Int32
}

func (l *outlineSelectCounter) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	statement, _ := query()
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "SELECT") {
		l.selects.Add(1)
	}
}

func TestBaseReadingOutlinesBatchPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	baseRepo := NewDocumentBaseRepository(db)
	base, err := baseRepo.CreateBase(ctx, types.DocumentBase{ID: uuid.New(), Name: "outline batch"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		id := uuid.New()
		ids = append(ids, id)
		doc := sourceDocumentForCanonicalTest(id, "fixture.pdf", "pdf/"+id.String()+".pdf", "")
		doc.MarkdownStatus = types.DocumentStageSucceeded
		doc.MarkdownGeneration = 1
		if err := db.Create(&doc).Error; err != nil {
			t.Fatal(err)
		}
		membership := types.DocumentBaseFile{DocumentBaseID: base.ID, DocumentID: id, SortOrder: i}
		if err := baseRepo.AddDocument(ctx, membership); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			md := types.DocumentMarkdown{DocumentID: id, StorageKey: "markdown/" + id.String() + ".md", Filename: "fixture.md",
				SizeBytes: 0, RecognitionGeneration: 1, Metadata: types.JSON(`{}`),
				DocumentOutline: types.JSON(`{"schemaVersion":1,"source":"none","sections":[]}`)}
			if err := db.Create(&md).Error; err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			if _, err := NewNoteReadingRepository(db).Put(ctx, readingNoteManifest(id, "batch"), 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	counter := &outlineSelectCounter{Interface: gormlog.Default}
	repo := NewDocumentBaseRepository(db.Session(&gorm.Session{Logger: counter}))
	for _, size := range []int{1, 10} {
		counter.selects.Store(0)
		rows, total, err := repo.ListReadingOutlines(ctx, base.ID, 1, size)
		if err != nil || total != 3 || len(rows) != min(size, 3) || counter.selects.Load() != 3 {
			t.Fatalf("size=%d rows=%d total=%d selects=%d err=%v", size, len(rows), total, counter.selects.Load(), err)
		}
		if rows[0].DocumentID != ids[0] || rows[0].NoteRevision == nil || rows[0].SourceGeneration == nil ||
			!strings.Contains(rows[0].NoteStructureJSON, "storageKey") {
			t.Fatalf("joined projection lost manifests: %+v", rows[0])
		}
		if size > 1 && (rows[1].NoteRevision != nil || rows[2].SourceGeneration != nil) {
			t.Fatal("LEFT JOIN missing rows were not preserved")
		}
	}
	rows, total, err := repo.ListReadingOutlines(ctx, base.ID, 2, 2)
	if err != nil || total != 3 || len(rows) != 1 || rows[0].DocumentID != ids[2] {
		t.Fatalf("pagination: %+v %d %v", rows, total, err)
	}
	if _, _, err := repo.ListReadingOutlines(ctx, uuid.New(), 1, 20); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing base: %v", err)
	}
}
