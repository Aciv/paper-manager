package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// spanRepositoryStub records the narrow persistence interactions needed to
// prove SpanRecorder preserves parent links and can refresh a live operation.
type spanRepositoryStub struct {
	stored          types.DocumentProcessingSpan
	heartbeatSpanID uuid.UUID
	rows            []types.DocumentProcessingSpan
	upsertStatus    types.ProcessingSpanStatus
	finishedStatus  types.ProcessingSpanStatus
	finishedCode    string
	finishedReason  string
}

func (s *spanRepositoryStub) UpsertRunning(_ context.Context, span types.DocumentProcessingSpan) (types.DocumentProcessingSpan, error) {
	if span.ID == uuid.Nil {
		span.ID = uuid.New()
	}
	span.Status = s.upsertStatus
	if span.Status == "" {
		span.Status = types.ProcessingSpanRunning
	}
	s.stored = span
	return span, nil
}
func (s *spanRepositoryStub) Heartbeat(_ context.Context, id uuid.UUID) error {
	s.heartbeatSpanID = id
	return nil
}
func (s *spanRepositoryStub) Finish(_ context.Context, _ uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error {
	s.finishedStatus, s.finishedCode, s.finishedReason = status, code, reason
	return nil
}
func (s *spanRepositoryStub) ListCurrent(context.Context, uuid.UUID, []uuid.UUID, int) ([]types.DocumentProcessingSpan, error) {
	return append([]types.DocumentProcessingSpan(nil), s.rows...), nil
}
func (*spanRepositoryStub) ListTimeline(context.Context, uuid.UUID, int) ([]types.DocumentProcessingSpan, error) {
	return nil, nil
}
func (s *spanRepositoryStub) LatestAttempt(_ context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind) (int, error) {
	latest := 0
	for _, row := range s.rows {
		if row.DocumentID == documentID && row.Kind == kind && row.Attempt > latest {
			latest = row.Attempt
		}
	}
	return latest, nil
}
func (*spanRepositoryStub) HasActive(context.Context, uuid.UUID, ...types.ProcessingSpanKind) (bool, error) {
	return false, nil
}
func (*spanRepositoryStub) CancelActiveRun(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (*spanRepositoryStub) IsScopeCancelled(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID) (bool, error) {
	return false, nil
}
func (*spanRepositoryStub) AggregateParentDisplay(context.Context, uuid.UUID) (types.ProcessingSpanStatus, error) {
	return "", nil
}

var _ interfaces.DocumentProcessingSpanRepository = (*spanRepositoryStub)(nil)

func TestSpanRecorderBeginChildPersistsParentAndHeartbeats(t *testing.T) {
	repo := &spanRepositoryStub{}
	tracker := NewSpanRecorder(repo, nil)
	parentID, documentID := uuid.New(), uuid.New()
	spanID, err := tracker.BeginChildSpan(
		context.Background(), parentID, documentID, 3,
		types.ProcessingSpanCatalogProvider, types.ProcessingSpanScopeProvider,
		types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogProvider, "source", "openalex"), 2,
	)
	if err != nil {
		t.Fatalf("BeginChild() error = %v", err)
	}
	if repo.stored.ParentSpanID == nil || *repo.stored.ParentSpanID != parentID {
		t.Fatalf("parent span = %v, want %v", repo.stored.ParentSpanID, parentID)
	}
	if err := tracker.Heartbeat(context.Background(), spanID); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	if repo.heartbeatSpanID != spanID {
		t.Fatalf("heartbeat span = %v, want %v", repo.heartbeatSpanID, spanID)
	}
}

func TestSpanRecorderPersistsProcessingRunFromContext(t *testing.T) {
	repo := &spanRepositoryStub{}
	tracker := NewSpanRecorder(repo, nil)
	runID := uuid.New()
	if _, err := tracker.BeginSpan(withProcessingRun(context.Background(), runID), uuid.New(), 1,
		types.ProcessingSpanPipeline, types.ProcessingSpanScopeProcess,
		types.ComposeProcessingSpanScopeID(types.ProcessingSpanPipeline, runID.String()), 0); err != nil {
		t.Fatal(err)
	}
	if repo.stored.RunID == nil || *repo.stored.RunID != runID {
		t.Fatalf("stored run id = %v, want %v", repo.stored.RunID, runID)
	}
}

func TestSpanRecorderViewUsesRecognitionAttemptNotPaperRetryAttempt(t *testing.T) {
	documentID := uuid.New()
	tracker := NewSpanRecorder(&spanRepositoryStub{rows: []types.DocumentProcessingSpan{
		{DocumentID: documentID, Attempt: 2, Kind: types.ProcessingSpanRecognition},
		{DocumentID: documentID, Attempt: 99, Kind: types.ProcessingSpanMetadataExtract},
	}}, nil)
	view, err := tracker.GetRunView(context.Background(), types.Document{ID: documentID})
	if err != nil {
		t.Fatalf("GetView() error = %v", err)
	}
	if view.LatestAttempt != 2 {
		t.Fatalf("latest recognition attempt = %d, want 2", view.LatestAttempt)
	}
	if len(view.Stages) != 2 {
		t.Fatalf("view stages = %d, want independently retained parse and paper stages", len(view.Stages))
	}
}

func TestSpanRecorderDoesNotReopenTerminalSpan(t *testing.T) {
	repo := &spanRepositoryStub{upsertStatus: types.ProcessingSpanCancelled}
	recorder := NewSpanRecorder(repo, nil)
	spanID, err := recorder.BeginSpan(context.Background(), uuid.New(), 1,
		types.ProcessingSpanRecognition, types.ProcessingSpanScopeDocument,
		types.ComposeProcessingSpanScopeID(types.ProcessingSpanRecognition, uuid.NewString()), 1)
	if err == nil || spanID == uuid.Nil {
		t.Fatalf("BeginSpan() span=%v error=%v, want terminal rejection", spanID, err)
	}
}

func TestSpanRecorderPersistsOnlyCanonicalFailure(t *testing.T) {
	repo := &spanRepositoryStub{}
	recorder := NewSpanRecorder(repo, nil)
	raw := "postgres://admin:secret@db.internal/papers provider body=<html>private</html>"
	if err := recorder.FinishSpan(context.Background(), uuid.New(), types.ProcessingSpanFailed, "DATABASE_FAILURE", raw); err != nil {
		t.Fatal(err)
	}
	if repo.finishedStatus != types.ProcessingSpanFailed || repo.finishedCode != "TASK_FAILED" ||
		repo.finishedReason != "processing task failed" {
		t.Fatalf("persisted failure = %q %q %q", repo.finishedStatus, repo.finishedCode, repo.finishedReason)
	}
	code, reason := spanFailure(fmt.Errorf("query failed: %w: %s", errors.New("database unavailable"), raw))
	if code != "TASK_FAILED" || strings.Contains(reason, "secret") || strings.Contains(reason, "internal") {
		t.Fatalf("classified failure = %q %q", code, reason)
	}
}

func TestSpanRecorderRunViewRedactsPersistedLegacyErrors(t *testing.T) {
	documentID, runID := uuid.New(), uuid.New()
	raw := "https://token@example.internal/api response body private"
	runs := newProcessingRunMemoryRepository()
	runs.runs[runID] = types.ProcessingRun{
		ID: runID, OwnerDocumentID: documentID, WorkflowKind: types.ProcessingWorkflowDocument,
		State: types.ProcessingRunFailed, ErrorCode: "DATABASE_ERROR", ErrorReason: raw,
	}
	recorder := NewSpanRecorder(&spanRepositoryStub{rows: []types.DocumentProcessingSpan{{
		ID: uuid.New(), DocumentID: documentID, Attempt: 1, Kind: types.ProcessingSpanRecognition,
		Status: types.ProcessingSpanFailed, ErrorCode: "PROVIDER_BODY", ErrorReason: raw,
		Attributes: types.JSON(`{"providerResponse":"private"}`),
	}}}, runs)
	view, err := recorder.GetRunView(context.Background(), types.Document{ID: documentID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "example.internal") || strings.Contains(string(encoded), "private") {
		t.Fatalf("processing view leaked persisted error: %s", encoded)
	}
	if len(view.Runs) != 1 || len(view.Stages) != 1 || view.Runs[0].ErrorCode != "TASK_FAILED" || view.Stages[0].ErrorCode != "TASK_FAILED" {
		t.Fatalf("sanitized processing view = %#v", view)
	}
}
