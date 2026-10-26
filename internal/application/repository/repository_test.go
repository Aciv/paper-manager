package repository

import (
	"context"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"net/url"
	"os"
	"paperMaster/internal/config"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from factory_test.go.
// ---------------------------------------------------------------------------

// TestNewDatabaseRejectsUnknownDriver verifies that database construction fails
// fast with an unsupported driver instead of returning a usable handle.
func TestNewDatabaseRejectsUnknownDriver(t *testing.T) {
	_, err := NewDatabase(config.Config{DatabaseDriver: "unknown"})
	if err == nil {
		t.Fatal("NewDatabase() error = nil, want unsupported driver error")
	}
}

// TestPostgresDSNWithStatementTimeout verifies that timeout injection works for
// URL and keyword DSNs without duplicating an existing value, while a zero
// timeout leaves the DSN untouched.
func TestPostgresDSNWithStatementTimeout(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		timeout time.Duration
		want    string
	}{
		{
			name:    "url gains parameter",
			dsn:     "postgres://papers:secret@localhost:5432/papers?sslmode=disable",
			timeout: 30 * time.Second,
			want:    "statement_timeout=30000",
		},
		{
			name:    "explicit url parameter wins",
			dsn:     "postgres://papers@localhost/papers?statement_timeout=5000",
			timeout: 30 * time.Second,
			want:    "statement_timeout=5000",
		},
		{
			name:    "key value gains parameter",
			dsn:     "host=localhost dbname=papers",
			timeout: 1500 * time.Millisecond,
			want:    "statement_timeout=1500",
		},
		{
			name:    "zero timeout leaves dsn alone",
			dsn:     "postgres://papers@localhost/papers",
			timeout: 0,
			want:    "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := postgresDSNWithStatementTimeout(test.dsn, test.timeout)
			if test.want == "" {
				if got != test.dsn {
					t.Fatalf("dsn = %q, want unchanged %q", got, test.dsn)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("dsn = %q, want it to contain %q", got, test.want)
			}
			if strings.Count(got, "statement_timeout=") != 1 {
				t.Fatalf("dsn = %q, statement_timeout must appear exactly once", got)
			}
		})
	}
}

// TestNewDatabasePoolConfigurationPostgres verifies that NewDatabase applies the
// configured maximum open connections and statement timeout to a real
// PostgreSQL pool.
func TestNewDatabasePoolConfigurationPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	db, err := NewDatabase(config.Config{
		DatabaseDriver:     "postgres",
		DatabaseURL:        dsn,
		DBMaxOpenConns:     7,
		DBMaxIdleConns:     3,
		DBStatementTimeout: 1500 * time.Millisecond,
		AsynqConcurrency:   2,
		AutoMigrate:        false,
	})
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer func() { _ = CloseDatabase(db) }()

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("max open connections = %d, want 7", got)
	}
	var timeout string
	if err := db.Raw("SHOW statement_timeout").Scan(&timeout).Error; err != nil {
		t.Fatalf("show statement_timeout: %v", err)
	}
	if timeout != "1500ms" {
		t.Fatalf("statement_timeout = %q, want 1500ms", timeout)
	}
}

// TestStatementTimeoutCancelsSlowQueryPostgres verifies that a configured
// statement timeout aborts an overlong query and surfaces a timeout error before
// the query can complete.
func TestStatementTimeoutCancelsSlowQueryPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	db, err := NewDatabase(config.Config{
		DatabaseDriver:     "postgres",
		DatabaseURL:        dsn,
		DBMaxOpenConns:     4,
		DBMaxIdleConns:     2,
		DBStatementTimeout: 200 * time.Millisecond,
		AsynqConcurrency:   1,
		AutoMigrate:        false,
	})
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer func() { _ = CloseDatabase(db) }()

	started := time.Now()
	err = db.Exec("SELECT pg_sleep(2)").Error
	if err == nil {
		t.Fatal("slow query succeeded, want statement timeout")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("slow query ran for %s, statement timeout was not applied", elapsed)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "statement timeout") &&
		!strings.Contains(err.Error(), "57014") {
		t.Fatalf("slow query error = %v, want statement timeout", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from postgres_test.go.
// ---------------------------------------------------------------------------

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
