package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type paperInfoQueueStub struct {
	tasks []*asynq.Task
	err   error
}

func (q *paperInfoQueueStub) EnqueueContext(
	_ context.Context,
	task *asynq.Task,
	_ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	q.tasks = append(q.tasks, task)
	if q.err != nil {
		return nil, q.err
	}
	return &asynq.TaskInfo{ID: "queued"}, nil
}

type paperInfoInspectorStub struct{ queued map[string]bool }

func (*paperInfoInspectorStub) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (*paperInfoInspectorStub) CancelReferenceTasksForRun(context.Context, string, string) (int, int, error) {
	return 0, 0, nil
}
func (*paperInfoInspectorStub) CancelReferenceTasksForDocuments(context.Context, []string) (int, int, error) {
	return 0, 0, nil
}
func (*paperInfoInspectorStub) HasQueuedTasksForDocument(context.Context, string) (bool, error) {
	return false, nil
}
func (s *paperInfoInspectorStub) HasQueuedTaskTypeForDocument(
	_ context.Context,
	_ string,
	taskType string,
) (bool, error) {
	return s.queued[taskType], nil
}

func TestPaperInfoControlRetries(t *testing.T) {
	ctx := context.Background()
	documentID := uuid.New()
	markdown := []byte("# Paper\n")
	revision := testMarkdownSHA(markdown)
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, Status: types.DocumentStatusReady, DOI: "10.1000/example",
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{
		DocumentID: documentID, StorageKey: "markdown/paper.md", SHA256: revision,
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID, MetadataSource: types.PaperMetadataSourceExtraction,
	}
	queue := &paperInfoQueueStub{}
	inspector := &paperInfoInspectorStub{queued: map[string]bool{}}
	controls := newPaperInfoServiceForTest(
		config.Config{},
		documents,
		nil,
		nil,
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref"}}},
		papers,
		queue,
		inspector,
		nil,
	)

	if err := controls.RetryExtraction(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryExtraction() error = %v", err)
	}
	if err := controls.RetryCatalogResolution(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryCatalogResolution() error = %v", err)
	}
	if len(queue.tasks) != 2 || queue.tasks[0].Type() != types.TypePaperMetadataExtract || queue.tasks[1].Type() != types.TypePaperCatalogResolve {
		t.Fatalf("queued tasks = %#v", queue.tasks)
	}
	var extraction types.PaperMetadataExtractPayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &extraction); err != nil {
		t.Fatal(err)
	}
	var catalog types.PaperCatalogResolvePayload
	if err := json.Unmarshal(queue.tasks[1].Payload(), &catalog); err != nil {
		t.Fatal(err)
	}
	if extraction.Attempt <= 0 || catalog.DocumentID != documentID.String() || catalog.IsReference {
		t.Fatalf("manual retries = extraction:%#v catalog:%#v", extraction, catalog)
	}
}

func TestPaperInfoTaskIDUsesManualAttempt(t *testing.T) {
	base := paperInfoTaskID(types.TypePaperCatalogResolve, "document-id", "revision", 0)
	firstRetry := paperInfoTaskID(types.TypePaperCatalogResolve, "document-id", "revision", 1)
	secondRetry := paperInfoTaskID(types.TypePaperCatalogResolve, "document-id", "revision", 2)
	if base == firstRetry || firstRetry == secondRetry {
		t.Fatalf("task IDs must distinguish manual attempts: %q, %q, %q", base, firstRetry, secondRetry)
	}
}

func TestEnqueueManagedTaskReturnsTaskIDConflict(t *testing.T) {
	err := enqueueManagedTask(
		context.Background(),
		&paperInfoQueueStub{err: asynq.ErrTaskIDConflict},
		asynq.NewTask(types.TypePaperCatalogResolve, nil),
		nil,
		"document-id",
	)
	if !errors.Is(err, asynq.ErrTaskIDConflict) {
		t.Fatalf("enqueueManagedTask() error = %v, want ErrTaskIDConflict", err)
	}
}

func TestPaperInfoControlRejectsUnknownSourceCatalogRetry(t *testing.T) {
	ctx := context.Background()
	placeholderID := uuid.New()
	citingID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: placeholderID, Status: types.DocumentStatusUnknown},
		types.Document{ID: citingID, Status: types.DocumentStatusReady},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.citations[citingID.String()] = []types.PaperCitation{{
		CitingDocumentID: citingID, CitedDocumentID: placeholderID, RawReference: "A referenced paper.",
	}}
	queue := &paperInfoQueueStub{}
	controls := newPaperInfoServiceForTest(
		config.Config{},
		documents,
		nil,
		nil,
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref"}}},
		papers,
		queue,
		&paperInfoInspectorStub{queued: map[string]bool{}},
		nil,
	)

	if err := controls.RetryCatalogResolution(ctx, placeholderID.String()); !errors.Is(err, ErrPaperInfoCatalogUnavailable) {
		t.Fatalf("RetryCatalogResolution() error = %v, want ErrPaperInfoCatalogUnavailable", err)
	}
	if len(queue.tasks) != 0 {
		t.Fatalf("unknown source retry queued tasks = %#v", queue.tasks)
	}
}

func TestPaperInfoControlRetriesReferenceDispatch(t *testing.T) {
	ctx := context.Background()
	documentID := uuid.New()
	revision := testMarkdownSHA([]byte("# Paper\n"))
	documents := newPaperDocumentRepoStub(types.Document{ID: documentID, Status: types.DocumentStatusReady})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/paper.md", SHA256: revision}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID}
	queue := &paperInfoQueueStub{}
	controls := newPaperInfoServiceForTest(config.Config{}, documents, nil, nil, nil, papers, queue, &paperInfoInspectorStub{queued: map[string]bool{}}, nil)

	if err := controls.RetryReferenceResolution(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryReferenceResolution() error = %v", err)
	}
	if len(queue.tasks) != 1 || queue.tasks[0].Type() != types.TypePaperReferenceDispatch {
		t.Fatalf("queued tasks = %#v, want one reference dispatch task", queue.tasks)
	}
	var payload types.PaperReferenceDispatchPayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DocumentID != documentID.String() || payload.MarkdownSHA256 != revision || payload.ReferenceRunID == "" {
		t.Fatalf("reference retry payload = %#v", payload)
	}
}

func TestPaperInfoControlRejectsUnknownReferenceAsSourceRetry(t *testing.T) {
	ctx := context.Background()
	placeholderID := uuid.New()
	firstSourceID := uuid.New()
	secondSourceID := uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: placeholderID, Status: types.DocumentStatusUnknown},
		types.Document{ID: firstSourceID, Status: types.DocumentStatusReady},
		types.Document{ID: secondSourceID, Status: types.DocumentStatusReady},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.citations[firstSourceID.String()] = []types.PaperCitation{{
		CitingDocumentID: firstSourceID, CitedDocumentID: placeholderID, RawReference: "First reference.",
	}}
	papers.citations[secondSourceID.String()] = []types.PaperCitation{{
		CitingDocumentID: secondSourceID, CitedDocumentID: placeholderID, RawReference: "Second reference.",
	}}
	queue := &paperInfoQueueStub{}
	controls := newPaperInfoServiceForTest(
		config.Config{}, documents, nil, nil, &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{&paperInfoProviderStub{name: "crossref"}}}, papers, queue,
		&paperInfoInspectorStub{queued: map[string]bool{}}, nil,
	)
	if err := controls.RetryCatalogResolution(ctx, placeholderID.String()); !errors.Is(err, ErrPaperInfoCatalogUnavailable) {
		t.Fatalf("RetryCatalogResolution() error = %v, want ErrPaperInfoCatalogUnavailable", err)
	}
	if len(queue.tasks) != 0 {
		t.Fatalf("unknown source retry queued tasks = %#v", queue.tasks)
	}
}
