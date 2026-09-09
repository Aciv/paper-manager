package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type processingRunMemoryRepository struct {
	mu    sync.Mutex
	runs  map[uuid.UUID]types.ProcessingRun
	items map[string]types.ProcessingRunItem
}

func newProcessingRunMemoryRepository() *processingRunMemoryRepository {
	return &processingRunMemoryRepository{
		runs: make(map[uuid.UUID]types.ProcessingRun), items: make(map[string]types.ProcessingRunItem),
	}
}

func (r *processingRunMemoryRepository) CreateQueued(_ context.Context, run types.ProcessingRun) (types.ProcessingRun, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stored := range r.runs {
		if stored.OwnerDocumentID == run.OwnerDocumentID && stored.WorkflowKind == run.WorkflowKind && stored.IdempotencyKey == run.IdempotencyKey {
			return stored, false, nil
		}
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	run.State = types.ProcessingRunQueued
	r.runs[run.ID] = run
	return run, true, nil
}

func (r *processingRunMemoryRepository) Get(_ context.Context, id uuid.UUID) (types.ProcessingRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return types.ProcessingRun{}, sql.ErrNoRows
	}
	return run, nil
}

func (r *processingRunMemoryRepository) transition(id uuid.UUID, from []types.ProcessingRunState, to types.ProcessingRunState) (types.ProcessingRun, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return types.ProcessingRun{}, false, sql.ErrNoRows
	}
	for _, allowed := range from {
		if run.State == allowed {
			run.State = to
			r.runs[id] = run
			return run, true, nil
		}
	}
	return run, false, nil
}

func (r *processingRunMemoryRepository) TryStart(_ context.Context, id uuid.UUID) (types.ProcessingRun, bool, error) {
	return r.transition(id, []types.ProcessingRunState{types.ProcessingRunQueued}, types.ProcessingRunRunning)
}

func (r *processingRunMemoryRepository) MarkRetryQueued(_ context.Context, id uuid.UUID, _, _ string) (types.ProcessingRun, bool, error) {
	return r.transition(id, []types.ProcessingRunState{types.ProcessingRunRunning}, types.ProcessingRunQueued)
}

func (r *processingRunMemoryRepository) RequestCancel(_ context.Context, id uuid.UUID, _, _ string) (types.ProcessingRun, bool, error) {
	return r.transition(id, []types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}, types.ProcessingRunCancelRequested)
}

func (r *processingRunMemoryRepository) RequestCancelActive(
	_ context.Context, owner uuid.UUID, kind types.ProcessingWorkflowKind, _, _ string,
) ([]types.ProcessingRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]types.ProcessingRun, 0)
	for id, run := range r.runs {
		if run.OwnerDocumentID != owner || run.WorkflowKind != kind || !run.State.Active() {
			continue
		}
		if run.State != types.ProcessingRunCancelRequested {
			run.State = types.ProcessingRunCancelRequested
			r.runs[id] = run
		}
		result = append(result, run)
	}
	for key, item := range r.items {
		if run, ok := r.runs[item.RunID]; ok && run.OwnerDocumentID == owner && run.WorkflowKind == kind &&
			(item.State == types.ProcessingRunQueued || item.State == types.ProcessingRunRunning) {
			item.State = types.ProcessingRunCancelled
			r.items[key] = item
		}
	}
	return result, nil
}

func (r *processingRunMemoryRepository) Finish(_ context.Context, id uuid.UUID, state types.ProcessingRunState, _, _ string) (types.ProcessingRun, bool, error) {
	var from []types.ProcessingRunState
	switch state {
	case types.ProcessingRunSucceeded:
		from = []types.ProcessingRunState{types.ProcessingRunRunning}
	case types.ProcessingRunFailed:
		from = []types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}
	case types.ProcessingRunCancelled:
		from = []types.ProcessingRunState{types.ProcessingRunCancelRequested}
	}
	return r.transition(id, from, state)
}

func (r *processingRunMemoryRepository) ListActive(_ context.Context, owner uuid.UUID, kinds ...types.ProcessingWorkflowKind) ([]types.ProcessingRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]types.ProcessingRun, 0)
	for _, run := range r.runs {
		if run.OwnerDocumentID != owner || !run.State.Active() {
			continue
		}
		if len(kinds) > 0 && run.WorkflowKind != kinds[0] {
			continue
		}
		result = append(result, run)
	}
	return result, nil
}

func (r *processingRunMemoryRepository) ListRecent(_ context.Context, owner uuid.UUID, limit int) ([]types.ProcessingRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]types.ProcessingRun, 0)
	for _, run := range r.runs {
		if run.OwnerDocumentID == owner && len(result) < limit {
			result = append(result, run)
		}
	}
	return result, nil
}

func (r *processingRunMemoryRepository) LatestAttempt(_ context.Context, owner uuid.UUID, kind types.ProcessingWorkflowKind) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var latest int64
	for _, run := range r.runs {
		if run.OwnerDocumentID == owner && run.WorkflowKind == kind && run.Attempt > latest {
			latest = run.Attempt
		}
	}
	return latest, nil
}

func processingRunMemoryItemKey(runID uuid.UUID, itemKey string) string {
	return runID.String() + ":" + itemKey
}

func (r *processingRunMemoryRepository) RegisterItem(_ context.Context, runID uuid.UUID, itemKey string) (types.ProcessingRunItem, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := processingRunMemoryItemKey(runID, itemKey)
	if item, ok := r.items[key]; ok {
		return item, false, nil
	}
	item := types.ProcessingRunItem{RunID: runID, ItemKey: itemKey, State: types.ProcessingRunQueued}
	r.items[key] = item
	return item, true, nil
}

func (r *processingRunMemoryRepository) TryStartItem(_ context.Context, runID uuid.UUID, itemKey string) (types.ProcessingRunItem, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := processingRunMemoryItemKey(runID, itemKey)
	item, ok := r.items[key]
	if !ok {
		return types.ProcessingRunItem{}, false, sql.ErrNoRows
	}
	if item.State != types.ProcessingRunQueued || r.runs[runID].State != types.ProcessingRunRunning {
		return item, false, nil
	}
	item.State = types.ProcessingRunRunning
	r.items[key] = item
	return item, true, nil
}

func (r *processingRunMemoryRepository) MarkItemRetry(_ context.Context, runID uuid.UUID, itemKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := processingRunMemoryItemKey(runID, itemKey)
	item := r.items[key]
	if item.State == types.ProcessingRunRunning {
		item.State = types.ProcessingRunQueued
		r.items[key] = item
	}
	return nil
}

func (r *processingRunMemoryRepository) FinishItem(_ context.Context, runID uuid.UUID, itemKey string, state types.ProcessingRunState, _, _ string) (types.ProcessingRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := processingRunMemoryItemKey(runID, itemKey)
	item, ok := r.items[key]
	if !ok {
		return types.ProcessingRun{}, sql.ErrNoRows
	}
	if item.State == types.ProcessingRunQueued || item.State == types.ProcessingRunRunning {
		item.State = state
		r.items[key] = item
	}
	r.finalizeMemoryItems(runID)
	return r.runs[runID], nil
}

func (r *processingRunMemoryRepository) SealItems(_ context.Context, runID uuid.UUID) (types.ProcessingRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[runID]
	now := time.Now()
	run.ItemsSealedAt = &now
	r.runs[runID] = run
	r.finalizeMemoryItems(runID)
	return r.runs[runID], nil
}

func (r *processingRunMemoryRepository) finalizeMemoryItems(runID uuid.UUID) {
	run := r.runs[runID]
	if run.ItemsSealedAt == nil || (run.State != types.ProcessingRunQueued && run.State != types.ProcessingRunRunning) {
		return
	}
	failed := false
	for _, item := range r.items {
		if item.RunID != runID {
			continue
		}
		if item.State == types.ProcessingRunQueued || item.State == types.ProcessingRunRunning {
			return
		}
		failed = failed || item.State == types.ProcessingRunFailed || item.State == types.ProcessingRunCancelled
	}
	if failed {
		run.State = types.ProcessingRunFailed
	} else {
		run.State = types.ProcessingRunSucceeded
	}
	r.runs[runID] = run
}

func TestProcessingRunRetryReusesIdentityAndCannotReopenTerminal(t *testing.T) {
	runs := newProcessingRunMemoryRepository()
	ownerID, runID := uuid.New(), uuid.New()
	_, _, _ = runs.CreateQueued(context.Background(), types.ProcessingRun{
		ID: runID, OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowDocument,
		Attempt: 1, IdempotencyKey: "retry-run",
	})
	ctx, controlled, err := startProcessingRun(context.Background(), runs, runID)
	if err != nil || !controlled {
		t.Fatalf("startProcessingRun() = controlled %v, error %v", controlled, err)
	}
	finishProcessingRunTask(ctx, runs, runID, controlled, errors.New("temporary"), true, "TEMPORARY", "temporary failure")
	run, _ := runs.Get(context.Background(), runID)
	if run.State != types.ProcessingRunQueued {
		t.Fatalf("retry run state = %q, want queued", run.State)
	}
	_, controlled, err = startProcessingRun(context.Background(), runs, runID)
	if err != nil || !controlled {
		t.Fatalf("retry start = controlled %v, error %v", controlled, err)
	}
	finishProcessingRunTask(context.Background(), runs, runID, controlled,
		fmt.Errorf("%w: permanent", asynq.SkipRetry), true, "PERMANENT", "permanent failure")
	run, _ = runs.Get(context.Background(), runID)
	if run.State != types.ProcessingRunFailed {
		t.Fatalf("terminal run state = %q, want failed", run.State)
	}
	if _, _, err := startProcessingRun(context.Background(), runs, runID); !errors.Is(err, asynq.RevokeTask) {
		t.Fatalf("terminal restart error = %v, want RevokeTask", err)
	}
}

func TestInterruptedRunItemRetriesUnlessRunWasCancelled(t *testing.T) {
	runs := newProcessingRunMemoryRepository()
	ownerID, runID := uuid.New(), uuid.New()
	_, _, _ = runs.CreateQueued(context.Background(), types.ProcessingRun{
		ID: runID, OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowReferenceDownload,
		Attempt: 1, IdempotencyKey: "interrupted-item",
	})
	_, _, _ = runs.TryStart(context.Background(), runID)
	_, _, _ = runs.RegisterItem(context.Background(), runID, "target")
	controlled, err := startProcessingRunItem(context.Background(), runs, runID, "target")
	if err != nil || !controlled {
		t.Fatalf("start item controlled=%t error=%v", controlled, err)
	}
	finishProcessingRunItemTask(context.Background(), runs, runID, "target", controlled, context.Canceled)
	item := runs.items[processingRunMemoryItemKey(runID, "target")]
	if item.State != types.ProcessingRunQueued {
		t.Fatalf("interrupted item state = %q, want queued", item.State)
	}
}

func TestEnqueueDocumentRecognitionUsesStageGenerationWithoutRun(t *testing.T) {
	documentID := uuid.New()
	documents := &documentStatusRepoStub{document: types.Document{ID: documentID, Status: types.DocumentStatusUploaded}}
	enqueuer := &documentTaskEnqueuerStub{}
	enqueuer.onEnqueue = func(task *asynq.Task) {
		var payload types.DocumentProcessPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if task.Type() != types.TypeDocumentRecognize || payload.Stage != types.DocumentStageMarkdown || payload.Generation != 1 {
			t.Fatalf("new recognition payload = %#v type=%s", payload, task.Type())
		}
	}
	service := &documentService{repo: documents, taskEnqueuer: enqueuer}
	if err := service.enqueueDocumentRecognition(context.Background(), documentID); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueDocumentRecognitionMarksStageFailedWhenEnqueueFails(t *testing.T) {
	documentID := uuid.New()
	documents := &documentStatusRepoStub{document: types.Document{ID: documentID, Status: types.DocumentStatusUploaded}}
	enqueuer := &documentTaskEnqueuerStub{enqueueErr: errors.New("queue unavailable")}
	service := &documentService{repo: documents, taskEnqueuer: enqueuer}
	if err := service.enqueueDocumentRecognition(context.Background(), documentID); err == nil {
		t.Fatal("enqueueDocumentRecognition() error = nil")
	}
	if len(enqueuer.tasks) != 1 {
		t.Fatalf("enqueued task attempts = %d, want 1", len(enqueuer.tasks))
	}
	if documents.document.MarkdownStatus != types.DocumentStageFailed || documents.document.Status != types.DocumentStatusFailed {
		t.Fatalf("document after queue failure = %#v", documents.document)
	}
}
