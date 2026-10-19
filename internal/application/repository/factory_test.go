package repository

import (
	"os"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/config"
)

func TestNewDatabaseRejectsUnknownDriver(t *testing.T) {
	_, err := NewDatabase(config.Config{DatabaseDriver: "unknown"})
	if err == nil {
		t.Fatal("NewDatabase() error = nil, want unsupported driver error")
	}
}

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
