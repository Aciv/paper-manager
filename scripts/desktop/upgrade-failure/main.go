// Command upgrade-failure is a validation-only candidate server. It refuses
// mutation without matching synthetic-fixture tokens in both disk and database.
// It is shipped in validation/, never as an application runtime component.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := failCandidate(); err != nil {
		fmt.Fprintln(os.Stderr, "synthetic upgrade failure probe:", err)
	}
	os.Exit(42)
}

func failCandidate() error {
	uploads := os.Getenv("UPLOAD_DIR")
	if !filepath.IsAbs(uploads) || filepath.Base(uploads) != "uploads" {
		return fmt.Errorf("not a fixture upload directory")
	}
	root := filepath.Dir(uploads)
	marker, err := os.ReadFile(filepath.Join(root, ".d4-synthetic-fixture"))
	if err != nil {
		return fmt.Errorf("fixture marker missing")
	}
	token := strings.TrimSpace(string(marker))
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid fixture token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("cannot connect to fixture database")
	}
	defer db.Close(ctx)
	var databaseToken string
	if err := db.QueryRow(ctx, "SELECT token FROM d4_upgrade_fixture LIMIT 1").Scan(&databaseToken); err != nil ||
		databaseToken != token {
		return fmt.Errorf("database is not this synthetic fixture")
	}
	if _, err := db.Exec(ctx, "CREATE TABLE d4_failed_migration (value integer)"); err != nil {
		return fmt.Errorf("cannot inject test migration")
	}
	for _, name := range []string{"notes/d4-canary.md", "uploads/d4-canary.txt"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("failed candidate wrote here"), 0600); err != nil {
			return fmt.Errorf("cannot write fixture canary")
		}
	}
	return fmt.Errorf("intentional post-migration health failure")
}
