package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

type digestOmittingStorage struct {
	interfaces.FileStorage
	opens int
}

func (s *digestOmittingStorage) Save(ctx context.Context, upload interfaces.FileUpload) (interfaces.StoredFile, error) {
	file, err := s.FileStorage.Save(ctx, upload)
	file.SHA256 = ""
	return file, err
}

func (s *digestOmittingStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	s.opens++
	return s.FileStorage.Open(ctx, key)
}

func TestDocumentUploadReadsSourceOnceAndRemovesDuplicateCopy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "stream_digest"
		if legacy {
			name = "legacy_fallback"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			local, err := fileservice.NewLocalStorage(root, 1024)
			if err != nil {
				t.Fatal(err)
			}
			var storage interfaces.FileStorage = local
			old := &digestOmittingStorage{FileStorage: local}
			if legacy {
				storage = old
			}
			repo := &documentBatchRepoStub{}
			s := NewDocumentService(repo, storage).(*documentService)
			opens := 0
			source := documentUploadSource{Filename: "paper.txt", Size: 5, Open: func() (io.ReadCloser, error) {
				opens++
				return io.NopCloser(bytes.NewBufferString("paper")), nil
			}}
			first, err := s.createDocumentFromSource(context.Background(), source, "")
			if err != nil || first.FileSHA256 == "" || opens != 1 {
				t.Fatalf("initial upload: %v, source opens=%d", err, opens)
			}
			second, err := s.createDocumentFromSource(context.Background(), source, "")
			if !errors.Is(err, types.ErrDuplicateFileHash) || second == nil || second.ID != first.ID || opens != 2 {
				t.Fatalf("duplicate changed identity or reread source: %v, opens=%d", err, opens)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || len(repo.documents) != 1 {
				t.Fatal("duplicate upload left stored copies")
			}
			if legacy && old.opens != 2 {
				t.Fatalf("legacy fallback reads=%d", old.opens)
			}
		})
	}
}

func TestDocumentDeduplicationFailureRemovesNewStoredFile(t *testing.T) {
	root := t.TempDir()
	storage, err := fileservice.NewLocalStorage(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	repo := &documentBatchRepoStub{failSearchAt: 1, searchErr: errors.New("lookup unavailable")}
	s := NewDocumentService(repo, storage).(*documentService)
	_, err = s.createDocumentFromSource(context.Background(), documentUploadSource{Filename: "paper.txt", Size: 5,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewBufferString("paper")), nil }}, "")
	if !errors.Is(err, errDocumentDuplicateLookup) {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || len(repo.documents) != 0 {
		t.Fatal("failed lookup left an orphan file or document")
	}
}
