package repository

import (
	"testing"

	"paperMaster/internal/config"
)

func TestNewDatabaseRejectsUnknownDriver(t *testing.T) {
	_, err := NewDatabase(config.Config{DatabaseDriver: "unknown"})
	if err == nil {
		t.Fatal("NewDatabase() error = nil, want unsupported driver error")
	}
}
