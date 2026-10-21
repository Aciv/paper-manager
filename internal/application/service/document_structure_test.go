package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDocumentStructureRebuildsMissingCorruptAndStaleCache(t *testing.T) {
	documentID := uuid.New()
	cacheDir := t.TempDir()
	outlineJSON, err := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}, Diagnostics: []string{"empty"}}).JSON()
	if err != nil {
		t.Fatal(err)
	}
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md",
			DocumentOutline: outlineJSON, RecognitionGeneration: 3},
		blocks: []types.Block{{ID: uuid.New(), DocumentID: documentID, Ordinal: 0, Type: "text"}},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: cacheDir,
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)

	first, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("first structure read: %v", err)
	}
	cachePath := filepath.Join(cacheDir, documentID.String()+".json")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache was not written: %v", err)
	}
	cached, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || cached.Fingerprint != first.Fingerprint || cached.SchemaVersion != 6 {
		t.Fatalf("cache hit = %#v, error=%v", cached, err)
	}
	if err := os.WriteFile(cachePath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || rebuilt.Fingerprint != first.Fingerprint || rebuilt.SchemaVersion != 6 {
		t.Fatalf("corrupt cache rebuild = %#v, error=%v", rebuilt, err)
	}
	cacheData, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	cacheData = bytes.Replace(cacheData, []byte(`"schemaVersion":6`), []byte(`"schemaVersion":5`), 1)
	if err := os.WriteFile(cachePath, cacheData, 0o600); err != nil {
		t.Fatal(err)
	}
	upgraded, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || upgraded.SchemaVersion != 6 {
		t.Fatalf("schema cache rebuild = %#v, error=%v", upgraded, err)
	}
	repository.markdown.RecognitionGeneration = 4
	staleRebuilt, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("stale cache rebuild: %v", err)
	}
	if staleRebuilt.Fingerprint == first.Fingerprint || staleRebuilt.RecognitionGeneration != 4 {
		t.Fatalf("stale cache was reused: %#v", staleRebuilt)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	cacheLossRebuilt, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || cacheLossRebuilt.Fingerprint != staleRebuilt.Fingerprint {
		t.Fatalf("cache loss rebuild = %#v, error=%v", cacheLossRebuilt, err)
	}
}

type blockingStructureRepository struct {
	*documentStatusRepoStub
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (r *blockingStructureRepository) GetMarkdown(ctx context.Context, documentID uuid.UUID) (types.DocumentMarkdown, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return types.DocumentMarkdown{}, ctx.Err()
	case <-r.release:
		return r.documentStatusRepoStub.GetMarkdown(ctx, documentID)
	}
}

func TestDocumentStructureSingleflightKeepsCompileAfterFirstCallerCancels(t *testing.T) {
	documentID := uuid.New()
	outline, _ := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}}).JSON()
	repository := &blockingStructureRepository{
		documentStatusRepoStub: &documentStatusRepoStub{
			document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable},
			markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md",
				DocumentOutline: outline, RecognitionGeneration: 1},
		},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: t.TempDir(),
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.GetDocumentReadingStructure(firstCtx, documentID.String())
		firstResult <- err
	}()
	<-repository.started

	secondResult := make(chan error, 1)
	secondInvoked := make(chan struct{})
	go func() {
		close(secondInvoked)
		_, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
		secondResult <- err
	}()
	<-secondInvoked
	select {
	case err := <-secondResult:
		t.Fatalf("second caller returned before shared compile was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller error = %v, want context.Canceled", err)
	}
	close(repository.release)
	if err := <-secondResult; err != nil {
		t.Fatalf("second caller did not receive shared result: %v", err)
	}
	if calls := repository.calls.Load(); calls != 1 {
		t.Fatalf("shared compile calls = %d, want 1", calls)
	}
}

func TestDocumentStructureResponseIsBounded(t *testing.T) {
	documentID := uuid.New()
	outline, _ := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}}).JSON()
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md", DocumentOutline: outline},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: t.TempDir(),
		DocumentStructureCacheEntries: 1, DocumentStructureCacheMaxBytes: 32}, repository)
	_, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if !errors.Is(err, ErrDocumentStructureTooLarge) {
		t.Fatalf("error = %v, want ErrDocumentStructureTooLarge", err)
	}
}

func TestDocumentStructureCacheWriteFailureIsNonFatal(t *testing.T) {
	documentID := uuid.New()
	parent := t.TempDir()
	cachePath := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(cachePath, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, _ := types.LegacyDocumentOutline().JSON()
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/legacy.md", DocumentOutline: legacy},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: cachePath,
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)
	structure, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("structure read must survive cache failure: %v", err)
	}
	if structure.Outline.Source != types.DocumentOutlineUnavailable {
		t.Fatalf("legacy outline source = %q", structure.Outline.Source)
	}
}

func TestDocumentStructureStartupEvictsOldestCacheFiles(t *testing.T) {
	cacheDir := t.TempDir()
	for index, name := range []string{"a.json", "b.json", "c.json"} {
		path := filepath.Join(cacheDir, name)
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(index+1), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	_ = NewDocumentStructureService(config.Config{DocumentStructureCacheDir: cacheDir,
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1024}, &documentStatusRepoStub{})
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "b.json" || entries[1].Name() != "c.json" {
		t.Fatalf("remaining cache files = %#v", entries)
	}
}
