package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"paperMaster/internal/application/repository"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/hibiken/asynq"
)

type importQueue struct {
	before func()
	types  []string
}

func (queue *importQueue) EnqueueContext(
	_ context.Context, task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	if queue.before != nil {
		queue.before()
	}
	queue.types = append(queue.types, task.Type())
	return &asynq.TaskInfo{ID: "import-test"}, nil
}

type failedImportMetadata struct{ interfaces.PaperService }

func (*failedImportMetadata) UpdateMetadata(
	context.Context, string, types.PaperMetadataUpdate,
) (*types.PaperMetadata, error) {
	return nil, errors.New("simulated metadata outage")
}

func TestDocumentImportWorkflowPostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := repository.NewDocumentRepository(db)
	metadata := repository.NewPaperMetadataRepository(db)
	papers := NewPaperService(documents, metadata, nil)
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "paper.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nimport-workflow"), 0o600); err != nil {
		t.Fatal(err)
	}
	queue := &importQueue{}
	service := &documentService{repo: documents, storage: storage, papers: papers, taskEnqueuer: queue}
	queue.before = func() {
		page, err := documents.Search(ctx, types.DocumentSearch{DOI: "10.1234/import"})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("document/DOI missing before task publication: %#v, %v", page, err)
		}
		item := page.Items[0]
		value, err := metadata.GetByDocumentID(ctx, item.ID)
		if err != nil || value.Title != "Imported title" || item.SourceLookupStatus != types.DocumentStageSkipped {
			t.Fatalf("metadata or skip state missing before publication: %#v, %#v, %v", item, value, err)
		}
	}
	options := types.DocumentImportOptions{
		Metadata: &types.PaperMetadataImport{DOI: "https://doi.org/10.1234/IMPORT",
			PaperMetadataUpdate: types.PaperMetadataUpdate{Title: importString("Imported title")}},
		Processing: types.DocumentImportProcessing{SourceLookup: types.SourceLookupSkip},
	}
	document, err := service.CreateDocumentFromLocalFile(ctx, path, "", options)
	if err != nil || document.ImportMetadata == nil || document.ImportMetadata.Status != "applied" {
		t.Fatalf("import = %#v, %v", document, err)
	}
	queue.before = nil
	if len(queue.types) != 1 || document.SourceLookupGeneration != 0 {
		t.Fatalf("unexpected tasks/generation: %#v, %#v", queue.types, document)
	}
	info := &paperInfoService{documents: documents, papers: papers, tasks: queue}
	if err := info.enqueueSourceLookupIfNeeded(ctx, document.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := info.restartSourceLookup(ctx, document.ID, ""); err != nil {
		t.Fatal(err)
	}
	if len(queue.types) != 1 {
		t.Fatal("automatic lookup ignored skip")
	}
	// Explicit catalog retry is allowed even though the saved mode stays skip.
	if err := info.RetryCatalogResolution(ctx, document.ID.String()); err != nil {
		t.Fatal(err)
	}
	state, err := documents.GetStage(ctx, document.ID, types.DocumentStageSourceLookup)
	if err != nil || state.Status != types.DocumentStageQueued || state.Generation != 1 || len(queue.types) != 2 {
		t.Fatalf("manual lookup = %#v, %v, tasks=%#v", state, err, queue.types)
	}
	if err := info.restartSourceLookup(ctx, document.ID, ""); err != nil || len(queue.types) != 2 {
		t.Fatalf("automatic retry interfered with manual task: %v", err)
	}
	options.Processing.SourceLookup = types.SourceLookupAuto
	options.Metadata.Title = importString("Replacement title")
	options.Metadata.Abstract = importString("Added abstract")
	duplicate, err := service.CreateDocumentFromLocalFile(ctx, path, "", options)
	if !errors.Is(err, types.ErrDuplicateDOI) || duplicate.ID != document.ID || len(queue.types) != 2 {
		t.Fatalf("duplicate import = %#v, %v, tasks=%#v", duplicate, err, queue.types)
	}
	stored, _ := documents.Get(ctx, document.ID)
	value, _ := metadata.GetByDocumentID(ctx, document.ID)
	if stored.SourceLookupMode != types.SourceLookupSkip || stored.SourceLookupGeneration != 1 ||
		value.Title != "Imported title" || value.Abstract != "Added abstract" {
		t.Fatalf("duplicate changed saved configuration or failed to fill metadata: %#v, %#v", stored, value)
	}
	options.DuplicateMetadata = types.DuplicateMetadataReplace
	if _, err := service.CreateDocumentFromLocalFile(ctx, path, "", options); !errors.Is(err, types.ErrDuplicateDOI) {
		t.Fatal(err)
	}
	value, _ = metadata.GetByDocumentID(ctx, document.ID)
	if value.Title != "Replacement title" {
		t.Fatal("explicit replacement was not applied")
	}
	options.DuplicateMetadata = types.DuplicateMetadataNone
	options.Metadata.Title = importString("Must not apply")
	duplicate, _ = service.CreateDocumentFromLocalFile(ctx, path, "", options)
	value, _ = metadata.GetByDocumentID(ctx, document.ID)
	if duplicate.ImportMetadata.Status != "skipped" || value.Title != "Replacement title" {
		t.Fatal("none policy applied metadata")
	}
}

func TestDocumentImportMetadataFailureKeepsSourcePostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "paper.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nmetadata-failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	queue := &importQueue{}
	service := &documentService{
		repo: repository.NewDocumentRepository(db), storage: storage,
		papers: &failedImportMetadata{}, taskEnqueuer: queue,
	}
	result, err := service.CreateDocumentsFromLocalFiles(context.Background(), []string{path}, nil,
		[]types.DocumentImportOptions{{Metadata: &types.PaperMetadataImport{
			PaperMetadataUpdate: types.PaperMetadataUpdate{Title: importString("Title")},
		}}})
	if err != nil || result.Status != "partial" || len(result.Items) != 1 {
		t.Fatalf("batch = %#v, %v", result, err)
	}
	item := result.Items[0]
	if item.Document == nil || item.Status != types.DocumentImportItemAccepted ||
		item.ImportMetadata.Status != "failed" || len(queue.types) != 1 {
		t.Fatalf("metadata failure discarded source/processing: %#v", item)
	}
	reader, err := storage.Open(context.Background(), item.Document.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
}
