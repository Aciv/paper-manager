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
