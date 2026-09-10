package repository

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDocumentProcessingSpanViewPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documentID := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key)
		VALUES (?, 'span-view.pdf', 'application/pdf', 1, ?)`,
		documentID, "phase4/"+documentID.String()).Error; err != nil {
		t.Fatalf("insert document: %v", err)
	}
	repo := NewDocumentProcessingSpanRepository(db)

	terminal, err := repo.UpsertRunning(ctx, types.DocumentProcessingSpan{
		DocumentID: documentID, Attempt: 1, Kind: types.ProcessingSpanRecognition,
		Scope: types.ProcessingSpanScopeDocument, ScopeID: "recognition:terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, terminal.ID, types.ProcessingSpanCancelled, "CANCELLED", "safe"); err != nil {
		t.Fatal(err)
	}
	reopened, err := repo.UpsertRunning(ctx, types.DocumentProcessingSpan{
		DocumentID: documentID, Attempt: 1, Kind: types.ProcessingSpanRecognition,
		Scope: types.ProcessingSpanScopeDocument, ScopeID: "recognition:terminal", RetryCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != terminal.ID || reopened.Status != types.ProcessingSpanCancelled {
		t.Fatalf("terminal span reopened: %#v", reopened)
	}
	if latest, err := repo.LatestAttempt(ctx, documentID, types.ProcessingSpanRecognition); err != nil || latest != 1 {
		t.Fatalf("LatestAttempt(recognition) = %d, error %v", latest, err)
	}
	now := time.Now().UTC()
	rows := make([]types.DocumentProcessingSpan, 0, 750)
	for scope := 0; scope < 150; scope++ {
		scopeID := types.ProcessingSpanScopeID("provider:" + strconv.Itoa(scope))
		for attempt := 1; attempt <= 5; attempt++ {
			rows = append(rows, types.DocumentProcessingSpan{
				ID: uuid.New(), DocumentID: documentID, Attempt: attempt,
				Kind: types.ProcessingSpanCatalogProvider, Scope: types.ProcessingSpanScopeProvider,
				ScopeID: scopeID,
				Status:  types.ProcessingSpanSucceeded, Attributes: types.JSON(`{}`),
				StartedAt: now.Add(time.Duration(scope*5+attempt) * time.Millisecond),
			})
		}
	}
	if err := db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatalf("insert span history: %v", err)
	}
	current, err := repo.ListCurrent(ctx, documentID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 100 {
		t.Fatalf("bounded current stages = %d, want 100", len(current))
	}
	timeline, err := repo.ListTimeline(ctx, documentID, 1000)
	if err != nil || len(timeline) != 100 {
		t.Fatalf("bounded timeline stages = %d, error %v", len(timeline), err)
	}

	var plan strings.Builder
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		result, err := tx.Raw(`EXPLAIN (COSTS OFF)
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY document_id, kind, scope, scope_id
					ORDER BY attempt DESC, updated_at DESC, id DESC
				) AS stage_rank
				FROM document_processing_spans WHERE document_id = ?
			) ranked WHERE stage_rank = 1 LIMIT 100`, documentID).Rows()
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var line string
			if err := result.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		return result.Err()
	})
	if err != nil {
		t.Fatalf("explain bounded span view: %v", err)
	}
	if !strings.Contains(plan.String(), "Index") {
		t.Fatalf("bounded span view did not use an index:\n%s", plan.String())
	}
}
