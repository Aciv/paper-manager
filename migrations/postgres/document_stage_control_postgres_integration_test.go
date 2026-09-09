package postgres

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	driver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDocumentStageControlMigrationUpgradeBackfillAndRollbackPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(driver.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("get admin sql.DB: %v", err)
	}
	defer adminSQL.Close()
	schema := "document_stage_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(driver.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get scoped sql.DB: %v", err)
	}
	defer sqlDB.Close()

	for _, migration := range migrations[:20] {
		if err := db.Exec(migration.up).Error; err != nil {
			t.Fatalf("apply prerequisite migration %d: %v", migration.version, err)
		}
	}
	artifactID, activeID, legacyReadyID := uuid.New(), uuid.New(), uuid.New()
	for id, status := range map[uuid.UUID]string{
		artifactID: "ready", activeID: "processing", legacyReadyID: "ready",
	} {
		if err := db.Exec(`INSERT INTO documents
			(id, original_filename, content_type, size_bytes, storage_key, status)
			VALUES (?, 'legacy.pdf', 'application/pdf', 1, ?, ?)`, id, "legacy/"+id.String(), status).Error; err != nil {
			t.Fatalf("insert legacy document: %v", err)
		}
		if err := db.Exec("INSERT INTO paper_metadata (document_id) VALUES (?)", id).Error; err != nil {
			t.Fatalf("insert legacy metadata: %v", err)
		}
	}
	if err := db.Exec(`INSERT INTO document_markdowns
		(document_id, storage_key, filename, size_bytes, updated_at)
		VALUES (?, ?, 'artifact.md', 10, '2026-01-02 03:04:05+00')`, artifactID, "markdown/"+artifactID.String()).Error; err != nil {
		t.Fatalf("insert Markdown artifact: %v", err)
	}
	if err := db.Exec(`UPDATE paper_metadata SET metadata_source = 'extraction',
		updated_at = '2026-01-02 03:05:06+00' WHERE document_id = ?`, artifactID).Error; err != nil {
		t.Fatalf("mark extraction artifact: %v", err)
	}
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, attempt, kind, scope, scope_id, status, started_at, updated_at)
		VALUES
		(?, ?, 7, 'recognition', 'document', 'recognition:active', 'running', CURRENT_TIMESTAMP, '2026-02-01 01:00:00+00'),
		(?, ?, 9, 'catalog_source', 'source', 'catalog_source:active', 'queued', CURRENT_TIMESTAMP, '2026-02-01 02:00:00+00')`,
		uuid.New(), activeID, uuid.New(), activeID).Error; err != nil {
		t.Fatalf("insert active spans: %v", err)
	}
	if err := db.Exec(`INSERT INTO processing_runs
		(id, owner_document_id, workflow_kind, idempotency_key, attempt, state, updated_at)
		VALUES (?, ?, 'catalog_reference', 'reference:active', 11, 'queued', '2026-02-01 03:00:00+00')`,
		uuid.New(), activeID).Error; err != nil {
		t.Fatalf("insert active reference run: %v", err)
	}

	if err := db.Exec(documentStageControlUp).Error; err != nil {
		t.Fatalf("apply document stage migration: %v", err)
	}
	type stageRow struct {
		MarkdownStatus               string
		MarkdownGeneration           int64
		MetadataExtractionStatus     string
		MetadataExtractionGeneration int64
		SourceLookupStatus           string
		SourceLookupGeneration       int64
		ReferenceLookupStatus        string
		ReferenceLookupGeneration    int64
	}
	load := func(id uuid.UUID) stageRow {
		t.Helper()
		var row stageRow
		if err := db.Table("documents").Where("id = ?", id).Take(&row).Error; err != nil {
			t.Fatalf("load stage row %s: %v", id, err)
		}
		return row
	}
	artifact := load(artifactID)
	if artifact.MarkdownStatus != "succeeded" || artifact.MarkdownGeneration != 1 ||
		artifact.MetadataExtractionStatus != "succeeded" || artifact.MetadataExtractionGeneration != 1 {
		t.Fatalf("artifact backfill = %#v", artifact)
	}
	active := load(activeID)
	if active.MarkdownStatus != "running" || active.MarkdownGeneration != 7 ||
		active.SourceLookupStatus != "queued" || active.SourceLookupGeneration != 9 ||
		active.ReferenceLookupStatus != "queued" || active.ReferenceLookupGeneration != 11 {
		t.Fatalf("active backfill = %#v", active)
	}
	legacyReady := load(legacyReadyID)
	if legacyReady.MarkdownStatus != "not_started" || legacyReady.MetadataExtractionStatus != "not_started" ||
		legacyReady.SourceLookupStatus != "not_started" || legacyReady.ReferenceLookupStatus != "not_started" {
		t.Fatalf("legacy ready row was overclaimed: %#v", legacyReady)
	}
	if err := db.Exec("UPDATE documents SET markdown_status = 'invalid' WHERE id = ?", artifactID).Error; err == nil {
		t.Fatal("invalid stage status should violate the check constraint")
	}
	var indexCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname IN ('idx_documents_markdown_stage_active',
		'idx_documents_metadata_extraction_stage_active',
		'idx_documents_source_lookup_stage_active',
		'idx_documents_reference_lookup_stage_active')`).Scan(&indexCount).Error; err != nil {
		t.Fatalf("inspect stage indexes: %v", err)
	}
	if indexCount != 4 {
		t.Fatalf("stage index count = %d, want 4", indexCount)
	}

	if err := db.Exec(documentStageControlDown).Error; err != nil {
		t.Fatalf("rollback document stage migration: %v", err)
	}
	var columnCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'documents'
		AND column_name IN ('markdown_status', 'metadata_extraction_status',
		'source_lookup_status', 'reference_lookup_status')`).Scan(&columnCount).Error; err != nil {
		t.Fatalf("inspect rolled back columns: %v", err)
	}
	if columnCount != 0 {
		t.Fatalf("stage status columns remaining after rollback = %d", columnCount)
	}
}
