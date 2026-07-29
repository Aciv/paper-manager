package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

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
	controls := NewPaperInfoService(
		config.Config{},
		documents,
		nil,
		nil,
		nil,
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
	if extraction.Attempt <= 0 || catalog.Attempt <= 0 || extraction.Attempt == catalog.Attempt {
		t.Fatalf("manual retry attempts = extraction:%d catalog:%d, want distinct positive values", extraction.Attempt, catalog.Attempt)
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

func TestPaperInfoControlAllowsUnknownCitationCatalogRetry(t *testing.T) {
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
	controls := NewPaperInfoService(
		config.Config{},
		documents,
		nil,
		nil,
		nil,
		papers,
		queue,
		&paperInfoInspectorStub{queued: map[string]bool{}},
		nil,
	)

	if err := controls.RetryCatalogResolution(ctx, placeholderID.String()); err != nil {
		t.Fatalf("RetryCatalogResolution() error = %v", err)
	}
	if len(queue.tasks) != 1 || queue.tasks[0].Type() != types.TypePaperCatalogResolve {
		t.Fatalf("queued tasks = %#v", queue.tasks)
	}
	var payload types.PaperCatalogResolvePayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Scope != types.PaperCatalogScopeReference || payload.DocumentID != citingID.String() || payload.CitedDocumentID != placeholderID.String() {
		t.Fatalf("unknown retry payload = %#v, want reference scope rooted at citing document", payload)
	}
}

func TestPaperInfoControlFansOutUnknownRetryAcrossIncomingCitations(t *testing.T) {
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
	controls := NewPaperInfoService(
		config.Config{}, documents, nil, nil, nil, papers, queue,
		&paperInfoInspectorStub{queued: map[string]bool{}}, nil,
	)
	if err := controls.RetryCatalogResolution(ctx, placeholderID.String()); err != nil {
		t.Fatalf("RetryCatalogResolution() error = %v", err)
	}
	if len(queue.tasks) != 2 {
		t.Fatalf("unknown retry queued %d tasks, want 2", len(queue.tasks))
	}
	seenSources := make(map[string]bool, 2)
	for _, task := range queue.tasks {
		var payload types.PaperCatalogResolvePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Scope != types.PaperCatalogScopeReference || payload.CitedDocumentID != placeholderID.String() {
			t.Fatalf("unknown retry payload = %#v, want reference task", payload)
		}
		seenSources[payload.DocumentID] = true
	}
	if !seenSources[firstSourceID.String()] || !seenSources[secondSourceID.String()] {
		t.Fatalf("unknown retry sources = %#v, want both incoming citations", seenSources)
	}
}
