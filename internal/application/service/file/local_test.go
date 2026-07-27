package file

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"paperMaster/internal/types/interfaces"
)

func TestLocalStorageSaveAndOpen(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	stored, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "../notes.json",
		Content:  strings.NewReader(`{"title":"paper"}`),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if stored.Filename != "notes.json" || stored.ContentType != "application/json" || stored.Size == 0 {
		t.Fatalf("unexpected stored file: %+v", stored)
	}

	opened, err := storage.Open(context.Background(), stored.Key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer opened.Close()
	content, err := io.ReadAll(opened)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(content), `{"title":"paper"}`; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestLocalStorageRejectsInvalidUploads(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		maxSize  int64
		wantErr  error
	}{
		{name: "unsupported extension", filename: "script.exe", content: "data", maxSize: 10, wantErr: ErrUnsupportedFileType},
		{name: "invalid PDF", filename: "paper.pdf", content: "not a PDF", maxSize: 10, wantErr: ErrInvalidPDF},
		{name: "invalid modern office file", filename: "paper.docx", content: "not a zip", maxSize: 20, wantErr: ErrInvalidOfficeFile},
		{name: "invalid legacy office file", filename: "paper.xls", content: "not a compound file", maxSize: 30, wantErr: ErrInvalidOfficeFile},
		{name: "too large", filename: "notes.txt", content: "12345", maxSize: 4, wantErr: ErrFileTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage, err := NewLocalStorage(t.TempDir(), test.maxSize)
			if err != nil {
				t.Fatalf("create storage: %v", err)
			}
			_, err = storage.Save(context.Background(), interfaces.FileUpload{
				Filename: test.filename,
				Content:  strings.NewReader(test.content),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestLocalStorageSaveBytesAndCopy(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "notes.txt", []byte("original"))
	if err != nil {
		t.Fatalf("save bytes: %v", err)
	}
	copy, err := storage.Copy(context.Background(), source)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copy.Key == source.Key || copy.Filename != source.Filename {
		t.Fatalf("unexpected copy: source=%+v copy=%+v", source, copy)
	}
	if err := storage.Delete(context.Background(), source.Key); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	reader, err := storage.Open(context.Background(), copy.Key)
	if err != nil {
		t.Fatalf("open copied file: %v", err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if got, want := string(content), "original"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestLocalStoragePlacesCategorizedFilesUnderTypeDirectory(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	stored, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "paper.pdf", Content: strings.NewReader("%PDF-1.7\n"), Category: interfaces.StorageCategoryPDF,
	})
	if err != nil {
		t.Fatalf("save categorized file: %v", err)
	}
	if !strings.HasPrefix(stored.Key, "pdf/") {
		t.Fatalf("storage key = %q, want pdf/ prefix", stored.Key)
	}
	if _, err := storage.Open(context.Background(), stored.Key); err != nil {
		t.Fatalf("open categorized file: %v", err)
	}
}
