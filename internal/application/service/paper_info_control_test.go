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
func (*paperInfoInspectorStub) CancelTasksForStage(context.Context, string, types.DocumentStage, int64) (int, int, error) {
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

func (*paperInfoInspectorStub) HasQueuedTaskForStage(context.Context, string, types.DocumentStage, int64) (bool, error) {
	return false, nil
}

func (*paperInfoInspectorStub) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (*paperInfoInspectorStub) SnapshotQueuedWork(context.Context) (types.QueuedWorkSnapshot, error) {
	return types.NewQueuedWorkSnapshot(), nil
}

func TestPaperInfoControlRetries(t *testing.T) {
	ctx := context.Background()
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/example",
	})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/paper.md"}
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
	if len(queue.tasks) != 2 || queue.tasks[0].Type() != types.TypePaperMetadataExtract || queue.tasks[1].Type() != types.TypePaperSourceQueryEnqueue {
		t.Fatalf("queued tasks = %#v", queue.tasks)
	}
	var extraction types.PaperMetadataExtractPayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &extraction); err != nil {
		t.Fatal(err)
	}
	var catalog types.PaperSourceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[1].Payload(), &catalog); err != nil {
		t.Fatal(err)
	}
	if extraction.Stage != types.DocumentStageMetadataExtraction || extraction.Generation != 1 ||
		catalog.DocumentID != documentID.String() ||
		catalog.Stage != types.DocumentStageSourceLookup || catalog.Generation != 1 {
		t.Fatalf("manual retries = extraction:%#v catalog:%#v", extraction, catalog)
	}
	document := documents.documents[documentID]
	document.MetadataExtractionStatus = types.DocumentStageFailed
	documents.documents[documentID] = document
	if err := controls.RetryExtraction(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryExtraction(second generation) error = %v", err)
	}
	var secondExtraction types.PaperMetadataExtractPayload
	if err := json.Unmarshal(queue.tasks[2].Payload(), &secondExtraction); err != nil {
		t.Fatal(err)
	}
	if secondExtraction.Generation != 2 {
		t.Fatalf("second user retry generation = %d, want 2", secondExtraction.Generation)
	}
}

func TestPaperInfoTaskIDUsesManualAttempt(t *testing.T) {
	base := paperInfoTaskID(types.TypePaperSourceQueryEnqueue, "document-id", 0)
	firstRetry := paperInfoTaskID(types.TypePaperSourceQueryEnqueue, "document-id", 1)
	secondRetry := paperInfoTaskID(types.TypePaperSourceQueryEnqueue, "document-id", 2)
	if base == firstRetry || firstRetry == secondRetry {
		t.Fatalf("task IDs must distinguish manual attempts: %q, %q, %q", base, firstRetry, secondRetry)
	}
}

func TestEnqueueManagedTaskReturnsTaskIDConflict(t *testing.T) {
	err := enqueueManagedTask(
		context.Background(),
		&paperInfoQueueStub{err: asynq.ErrTaskIDConflict},
		asynq.NewTask(types.TypePaperSourceQueryEnqueue, nil),
		nil,
		"document-id",
	)
	if !errors.Is(err, asynq.ErrTaskIDConflict) {
		t.Fatalf("enqueueManagedTask() error = %v, want ErrTaskIDConflict", err)
	}
}

func TestEnqueueDocumentStageTaskTreatsSameGenerationConflictAsSuccess(t *testing.T) {
	err := enqueueDocumentStageTask(
		context.Background(),
		&paperInfoQueueStub{err: asynq.ErrTaskIDConflict},
		asynq.NewTask(types.TypePaperSourceQueryEnqueue, nil),
		nil,
		"document-id",
	)
	if err != nil {
		t.Fatalf("enqueueDocumentStageTask() error = %v", err)
	}
}

func TestPaperInfoControlRetriesReferenceDispatch(t *testing.T) {
	ctx := context.Background()
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable})
	documents.markdowns[documentID] = types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/paper.md"}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID}
	queue := &paperInfoQueueStub{}
	controls := newPaperInfoServiceForTest(config.Config{}, documents, nil, nil, nil, papers, queue, &paperInfoInspectorStub{queued: map[string]bool{}}, nil)

	if err := controls.RetryReferenceResolution(ctx, documentID.String()); err != nil {
		t.Fatalf("RetryReferenceResolution() error = %v", err)
	}
	if len(queue.tasks) != 1 || queue.tasks[0].Type() != types.TypePaperReferenceQueryEnqueue {
		t.Fatalf("queued tasks = %#v, want one reference dispatch task", queue.tasks)
	}
	var payload types.PaperReferenceQueryEnqueuePayload
	if err := json.Unmarshal(queue.tasks[0].Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DocumentID != documentID.String() ||
		payload.Stage != types.DocumentStageReferenceLookup || payload.Generation != 1 {
		t.Fatalf("reference retry payload = %#v", payload)
	}
}
