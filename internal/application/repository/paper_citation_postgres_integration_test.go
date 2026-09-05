package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestPaperCitationRepositoryMergeAutomaticCitationsPostgres verifies the
// transaction/row-lock implementation against a real PostgreSQL database.
// It is deliberately opt-in: callers must point PAPERS_MANAGER_TEST_POSTGRES_DSN
// at a disposable test database. The test creates and drops its own schema,
// so it never reads or writes the application's public schema.
func TestPaperCitationRepositoryMergeAutomaticCitationsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL test sql.DB: %v", err)
	}
	// SET search_path is connection-local. A one-connection pool guarantees
	// the repository transaction below remains inside the disposable schema.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	defer sqlDB.Close()

	schema := "paper_citation_phase2_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if err := db.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatalf("set test schema: %v", err)
	}
	if err := db.Exec(`CREATE TABLE documents (id UUID PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create documents table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE paper_citations (
        citing_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
        cited_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
        raw_reference TEXT NOT NULL,
        origin TEXT NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'extraction')),
        created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY (citing_document_id, cited_document_id)
    )`).Error; err != nil {
		t.Fatalf("create paper_citations table: %v", err)
	}

	ctx := context.Background()
	citingID, resolvedID, unknownID, manualID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{citingID, resolvedID, unknownID, manualID} {
		if err := db.Exec("INSERT INTO documents (id) VALUES (?)", id).Error; err != nil {
			t.Fatalf("insert fixture document %s: %v", id, err)
		}
	}
	repo := &paperCitationRepository{db: db}
	raw := "[1] Ada Lovelace. Notes on the analytical engine."
	if err := repo.AddCitation(ctx, types.PaperCitation{
		CitingDocumentID: citingID, CitedDocumentID: unknownID,
		RawReference: raw, Origin: types.PaperCitationOriginExtraction,
	}); err != nil {
		t.Fatalf("seed automatic citation: %v", err)
	}
	if err := repo.RepointCitation(ctx, citingID, unknownID, resolvedID, raw); err != nil {
		t.Fatalf("resolve automatic citation: %v", err)
	}

	items, removed, err := repo.MergeAutomaticCitations(ctx, citingID, []types.PaperCitation{{
		CitingDocumentID: citingID, CitedDocumentID: unknownID,
		RawReference: " [1] ADA Lovelace. Notes on the analytical engine. ",
		Origin:       types.PaperCitationOriginExtraction,
	}})
	if err != nil {
		t.Fatalf("merge repeated automatic citation: %v", err)
	}
	if len(removed) != 0 || len(items) != 1 || items[0].CitedDocumentID != resolvedID {
		t.Fatalf("merge result items=%#v removed=%#v, want prior resolved target", items, removed)
	}

	if err := repo.AddCitation(ctx, types.PaperCitation{
		CitingDocumentID: citingID, CitedDocumentID: manualID,
		RawReference: "Manual reference", Origin: types.PaperCitationOriginManual,
	}); err != nil {
		t.Fatalf("seed manual citation: %v", err)
	}
	items, removed, err = repo.MergeAutomaticCitations(ctx, citingID, nil)
	if err != nil {
		t.Fatalf("merge detected empty bibliography: %v", err)
	}
	if len(items) != 1 || items[0].CitedDocumentID != manualID || items[0].EffectiveOrigin() != types.PaperCitationOriginManual {
		t.Fatalf("detected empty merge retained wrong rows: %#v", items)
	}
	if len(removed) != 1 || removed[0].CitedDocumentID != resolvedID {
		t.Fatalf("removed automatic rows = %#v, want resolved automatic row", removed)
	}

	var count int64
	if err := db.Table("paper_citations").Where("citing_document_id = ?", citingID).Count(&count).Error; err != nil {
		t.Fatalf("count final citations: %v", err)
	}
	if count != 1 {
		t.Fatalf("final citation row count = %d, want 1", count)
	}
}
