package repository

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openPostgresTestDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	ensurePostgresTrgmExtension(t, admin)
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL admin sql.DB: %v", err)
	}
	schema := "papers_manager_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		adminSQL.Close()
		t.Fatalf("create test schema: %v", err)
	}
	scopedDSN := postgresSearchPathDSN(t, dsn, schema)
	db, err := gorm.Open(postgres.Open(scopedDSN), &gorm.Config{})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		adminSQL.Close()
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get scoped PostgreSQL sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	if err := Migrate(context.Background(), db); err != nil {
		sqlDB.Close()
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		adminSQL.Close()
		t.Fatalf("migrate scoped PostgreSQL database: %v", err)
	}
	cleanup := func() {
		sqlDB.Close()
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminSQL.Close()
	}
	return db, cleanup
}

// ensurePostgresTrgmExtension pins pg_trgm to public before any test schema
// migration. Package tests run concurrently, so an advisory lock prevents two
// schemas from racing to install the extension in different schemas.
func ensurePostgresTrgmExtension(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_lock(?)", int64(2026101901)).Error; err != nil {
			return err
		}
		defer func() { _ = tx.Exec("SELECT pg_advisory_unlock(?)", int64(2026101901)).Error }()
		var schema string
		if err := tx.Raw(`SELECT n.nspname FROM pg_extension e
			JOIN pg_namespace n ON n.oid = e.extnamespace
			WHERE e.extname = 'pg_trgm'`).Scan(&schema).Error; err != nil {
			return err
		}
		if schema == "" {
			return tx.Exec("CREATE EXTENSION pg_trgm SCHEMA public").Error
		}
		if schema != "public" {
			return tx.Exec("ALTER EXTENSION pg_trgm SET SCHEMA public").Error
		}
		return nil
	}); err != nil {
		t.Fatalf("ensure pg_trgm extension: %v", err)
	}
}

func postgresSearchPathDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse PostgreSQL DSN: %v", err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schema
}
