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

func TestProcessingRunsMigrationUpgradeAndRollbackPostgres(t *testing.T) {
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
	schema := "processing_run_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if err := db.Exec(`CREATE TABLE documents (id UUID PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create documents: %v", err)
	}
	if err := db.Exec(`CREATE TABLE document_processing_spans (
		id UUID PRIMARY KEY,
		document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		attempt INTEGER NOT NULL CHECK (attempt > 0),
		kind TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT 'document',
		scope_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create legacy spans: %v", err)
	}
	if err := db.Exec(processingRunsUp).Error; err != nil {
		t.Fatalf("apply processing runs migration: %v", err)
	}
	if err := db.Exec(processingRunItemsAndSpanViewUp).Error; err != nil {
		t.Fatalf("apply processing run items migration: %v", err)
	}
	var dataType string
	if err := db.Raw(`SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_processing_spans' AND column_name = 'attempt'`).Scan(&dataType).Error; err != nil {
		t.Fatalf("inspect widened attempt: %v", err)
	}
	if dataType != "bigint" {
		t.Fatalf("attempt data type = %q, want bigint", dataType)
	}
	if err := db.Exec(processingRunItemsAndSpanViewDown).Error; err != nil {
		t.Fatalf("rollback processing run items migration: %v", err)
	}
	var itemTable *string
	if err := db.Raw("SELECT to_regclass('processing_run_items')").Scan(&itemTable).Error; err != nil {
		t.Fatalf("inspect rolled back run item table: %v", err)
	}
	if itemTable != nil {
		t.Fatalf("processing_run_items still exists after rollback: %q", *itemTable)
	}
	if err := db.Exec(processingRunsDown).Error; err != nil {
		t.Fatalf("rollback processing runs migration: %v", err)
	}
	var runTable *string
	if err := db.Raw("SELECT to_regclass('processing_runs')").Scan(&runTable).Error; err != nil {
		t.Fatalf("inspect rolled back table: %v", err)
	}
	if runTable != nil {
		t.Fatalf("processing_runs still exists after rollback: %q", *runTable)
	}
	dataType = ""
	if err := db.Raw(`SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_processing_spans' AND column_name = 'attempt'`).Scan(&dataType).Error; err != nil {
		t.Fatalf("inspect rollback attempt: %v", err)
	}
	if dataType != "bigint" {
		t.Fatalf("rollback narrowed attempt to %q, want bigint", dataType)
	}
}
