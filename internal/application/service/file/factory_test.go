package file

import (
	"testing"

	"paperMaster/internal/config"
)

func TestNewStorageSelectsLocalDriver(t *testing.T) {
	storage, err := NewStorage(config.Config{
		FileStorageDriver: "local",
		UploadDirectory:   t.TempDir(),
		MaxUploadSize:     1024,
	}, nil)
	if err != nil {
		t.Fatalf("NewStorage() error = %v", err)
	}
	if _, ok := storage.(*LocalStorage); !ok {
		t.Fatalf("NewStorage() type = %T, want *LocalStorage", storage)
	}
}

func TestNewStorageRejectsUnknownDriver(t *testing.T) {
	_, err := NewStorage(config.Config{FileStorageDriver: "unknown"}, nil)
	if err == nil {
		t.Fatal("NewStorage() error = nil, want unsupported driver error")
	}
}
