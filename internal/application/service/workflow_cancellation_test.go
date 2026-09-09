package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type cancellationTaskInspectorStub struct {
	runs            []types.ProcessingRun
	targets         []string
	deleted         int
	signalled       int
	err             error
	targetDeleted   int
	targetSignalled int
	targetErr       error
	onRun           func()
}

func (*cancellationTaskInspectorStub) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (s *cancellationTaskInspectorStub) CancelTasksForRun(
	_ context.Context, owner, runID string, kind types.ProcessingWorkflowKind,
) (int, int, error) {
	if s.onRun != nil {
		s.onRun()
	}
	s.runs = append(s.runs, types.ProcessingRun{ID: uuid.MustParse(runID), OwnerDocumentID: uuid.MustParse(owner), WorkflowKind: kind})
	return s.deleted, s.signalled, s.err
}
func (s *cancellationTaskInspectorStub) CancelReferenceTasksForDocuments(_ context.Context, ids []string) (int, int, error) {
	s.targets = append(s.targets, ids...)
	return s.targetDeleted, s.targetSignalled, s.targetErr
}
func (*cancellationTaskInspectorStub) HasQueuedTasksForDocument(context.Context, string) (bool, error) {
	return false, nil
}
func (*cancellationTaskInspectorStub) HasQueuedTaskTypeForDocument(context.Context, string, string) (bool, error) {
	return false, nil
}

type cancellationSchedulerStub struct {
	source  uuid.UUID
	runID   uuid.UUID
	targets []uuid.UUID
	err     error
}

func (*cancellationSchedulerStub) Register(context.Context, types.PaperReferenceJob) (bool, error) {
	return false, nil
}
func (*cancellationSchedulerStub) Claim(context.Context, uuid.UUID) (types.PaperReferenceClaim, error) {
	return types.PaperReferenceClaim{}, nil
}
func (*cancellationSchedulerStub) Report(context.Context, uuid.UUID, types.PaperCatalogProvider, types.PaperReferenceOutcome) (types.PaperReferenceReport, error) {
	return types.PaperReferenceReport{}, nil
}
func (*cancellationSchedulerStub) Cooldown(context.Context, types.PaperCatalogProvider, time.Duration) error {
	return nil
}
func (s *cancellationSchedulerStub) CancelRun(_ context.Context, source, runID uuid.UUID) ([]uuid.UUID, error) {
	s.source, s.runID = source, runID
	return append([]uuid.UUID(nil), s.targets...), s.err
}
func (*cancellationSchedulerStub) IsRunActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (*cancellationSchedulerStub) IsTargetActive(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}
func (*cancellationSchedulerStub) Progress(context.Context, uuid.UUID) (types.PaperReferenceProgress, error) {
	return types.PaperReferenceProgress{}, nil
}

func TestWorkflowCancellationFencesBeforeCatalogAdaptersAndFinishesRun(t *testing.T) {
	runs := newProcessingRunMemoryRepository()
	ownerID, runID, targetID := uuid.New(), uuid.New(), uuid.New()
	_, _, _ = runs.CreateQueued(context.Background(), types.ProcessingRun{
		ID: runID, OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowCatalogReference,
		Attempt: 1, IdempotencyKey: "catalog",
	})
	tasks := &cancellationTaskInspectorStub{deleted: 2, signalled: 1, targetDeleted: 1}
	tasks.onRun = func() {
		stored, err := runs.Get(context.Background(), runID)
		if err != nil {
			t.Fatalf("load run before queue adapter: %v", err)
		}
		if stored.State != types.ProcessingRunCancelRequested {
			t.Fatalf("run state before queue adapter = %q, want cancel_requested", stored.State)
		}
	}
	scheduler := &cancellationSchedulerStub{targets: []uuid.UUID{targetID}}
	service := NewWorkflowCancellationService(runs, tasks, scheduler)
	result, err := service.CancelActiveRuns(context.Background(), ownerID, types.ProcessingWorkflowCatalogReference)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RunIDs) != 1 || result.RunIDs[0] != runID || result.DeletedTasks != 2 ||
		result.SignalledTasks != 1 || result.ReleasedTargets != 0 {
		t.Fatalf("cancellation result = %#v", result)
	}
	if scheduler.source != uuid.Nil || scheduler.runID != uuid.Nil || len(tasks.targets) != 0 {
		t.Fatalf("target work must remain independent: %#v %#v", scheduler, tasks)
	}
	stored, _ := runs.Get(context.Background(), runID)
	if stored.State != types.ProcessingRunCancelled {
		t.Fatalf("run state = %q, want cancelled", stored.State)
	}
	second, err := service.CancelActiveRuns(context.Background(), ownerID, types.ProcessingWorkflowCatalogReference)
	if err != nil || len(second.RunIDs) != 0 || len(tasks.runs) != 1 {
		t.Fatalf("idempotent cancellation = %#v, error %v, calls %d", second, err, len(tasks.runs))
	}
}

func TestWorkflowCancellationKeepsFenceWhenQueueCleanupFails(t *testing.T) {
	runs := newProcessingRunMemoryRepository()
	ownerID, runID := uuid.New(), uuid.New()
	_, _, _ = runs.CreateQueued(context.Background(), types.ProcessingRun{
		ID: runID, OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowReferenceDownload,
		Attempt: 1, IdempotencyKey: "download",
	})
	queueErr := errors.New("redis unavailable")
	service := NewWorkflowCancellationService(runs, &cancellationTaskInspectorStub{err: queueErr}, nil)
	result, err := service.CancelActiveRuns(context.Background(), ownerID, types.ProcessingWorkflowReferenceDownload)
	if !errors.Is(err, queueErr) || len(result.RunIDs) != 1 {
		t.Fatalf("CancelActiveRuns() result=%#v error=%v", result, err)
	}
	stored, _ := runs.Get(context.Background(), runID)
	if stored.State != types.ProcessingRunCancelled {
		t.Fatalf("run state after adapter error = %q, want cancelled", stored.State)
	}
}
