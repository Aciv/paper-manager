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
func (s *spanRepositoryStub) ListCurrent(context.Context, uuid.UUID, int) ([]types.DocumentProcessingSpan, error) {
	return append([]types.DocumentProcessingSpan(nil), s.rows...), nil
}
func (s *spanRepositoryStub) ListCurrentBatch(
	_ context.Context, documentIDs []uuid.UUID, perDocumentLimit int,
) (map[uuid.UUID][]types.DocumentProcessingSpan, error) {
	result := make(map[uuid.UUID][]types.DocumentProcessingSpan, len(documentIDs))
	for _, id := range documentIDs {
		rows := make([]types.DocumentProcessingSpan, 0)
		for _, row := range s.rows {
			if row.DocumentID == id && len(rows) < perDocumentLimit {
				rows = append(rows, row)
			}
		}
		result[id] = rows
	}
	return result, nil
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
func (*spanRepositoryStub) CancelActiveStage(context.Context, uuid.UUID, types.DocumentStage, int64, string, string) error {
	return nil
}
func (*spanRepositoryStub) AggregateParentDisplay(context.Context, uuid.UUID) (types.ProcessingSpanStatus, error) {
	return "", nil
}

var _ interfaces.DocumentProcessingSpanRepository = (*spanRepositoryStub)(nil)

// TestSpanRecorderBeginChildPersistsParentAndHeartbeats verifies that a child
// span persists its parent reference and can be refreshed by its returned ID.
func TestSpanRecorderBeginChildPersistsParentAndHeartbeats(t *testing.T) {
	repo := &spanRepositoryStub{}
	tracker := NewSpanTracker(repo)
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

// TestSpanTrackerExposesNarrowWriterAndReaderViewsOfSameInstance verifies that
// the narrow writer and reader adapters share the original tracker instance.
func TestSpanTrackerExposesNarrowWriterAndReaderViewsOfSameInstance(t *testing.T) {
	tracker := NewSpanTracker(&spanRepositoryStub{})
	if NewSpanRecorder(tracker) != tracker || NewProcessingViewQuery(tracker) != tracker {
		t.Fatal("SpanTracker adapters must preserve the single implementation instance")
	}
}

// TestProcessingQueryKeepsBoundedObservationRows verifies that the view retains
// observation rows for each independently attempted stage kind.
func TestProcessingQueryKeepsBoundedObservationRows(t *testing.T) {
	documentID := uuid.New()
	tracker := NewSpanTracker(&spanRepositoryStub{rows: []types.DocumentProcessingSpan{
		{DocumentID: documentID, Attempt: 2, Kind: types.ProcessingSpanRecognition},
		{DocumentID: documentID, Attempt: 99, Kind: types.ProcessingSpanMetadataExtract},
	}})
	view, err := tracker.GetView(context.Background(), types.Document{ID: documentID})
	if err != nil {
		t.Fatalf("GetView() error = %v", err)
	}
	if len(view.Stages) != 2 {
		t.Fatalf("view stages = %d, want independently retained parse and paper stages", len(view.Stages))
	}
}

// TestProcessingQueryUsesCurrentStageControlState verifies that the view derives
// all four stage states from the document columns and sanitizes raw error text.
func TestProcessingQueryUsesCurrentStageControlState(t *testing.T) {
	documentID, canonicalID := uuid.New(), uuid.New()
	raw := "private provider response"
	view, err := NewSpanTracker(&spanRepositoryStub{}).GetView(context.Background(), types.Document{
		ID: documentID, SourceState: types.DocumentSourceMerged, CanonicalDocumentID: &canonicalID,
		MarkdownStatus: types.DocumentStageFailed, MarkdownGeneration: 4, MarkdownErrorCode: ptr("PROVIDER_BODY"), MarkdownErrorMessage: &raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.SourceState != types.DocumentSourceMerged || view.CanonicalDocumentID == nil || *view.CanonicalDocumentID != canonicalID || len(view.StageStates) != 4 {
		t.Fatalf("current control projection = %#v", view)
	}
	if view.StageStates[0].ErrorCode == nil || *view.StageStates[0].ErrorCode != types.ProcessingErrorTaskFailed || view.StageStates[0].ErrorMessage == nil || *view.StageStates[0].ErrorMessage != "processing task failed" {
		t.Fatalf("stage error was not sanitized: %#v", view.StageStates[0])
	}
}

func ptr(value string) *string { return &value }

// TestSpanRecorderDoesNotReopenTerminalSpan verifies that beginning a span fails
// when the repository reports that it is already in a terminal state.
func TestSpanRecorderDoesNotReopenTerminalSpan(t *testing.T) {
	repo := &spanRepositoryStub{upsertStatus: types.ProcessingSpanCancelled}
	recorder := NewSpanTracker(repo)
	spanID, err := recorder.BeginSpan(context.Background(), uuid.New(), 1,
		types.ProcessingSpanRecognition, types.ProcessingSpanScopeDocument,
		types.ComposeProcessingSpanScopeID(types.ProcessingSpanRecognition, uuid.NewString()), 1)
	if err == nil || spanID == uuid.Nil {
		t.Fatalf("BeginSpan() span=%v error=%v, want terminal rejection", spanID, err)
	}
}

// TestSpanRecorderPersistsOnlyCanonicalFailure verifies that finished spans and
// classified errors store only the canonical, redacted failure text.
func TestSpanRecorderPersistsOnlyCanonicalFailure(t *testing.T) {
	repo := &spanRepositoryStub{}
	recorder := NewSpanTracker(repo)
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

// TestProcessingQueryRedactsPersistedErrors verifies that serialized processing
// views omit raw provider error text, hosts, tokens, and attributes.
func TestProcessingQueryRedactsPersistedErrors(t *testing.T) {
	documentID := uuid.New()
	raw := "https://token@example.internal/api response body private"
	recorder := NewSpanTracker(&spanRepositoryStub{rows: []types.DocumentProcessingSpan{{
		ID: uuid.New(), DocumentID: documentID, Attempt: 1, Kind: types.ProcessingSpanRecognition,
		Status: types.ProcessingSpanFailed, ErrorCode: "PROVIDER_BODY", ErrorReason: raw,
		Attributes: types.JSON(`{"providerResponse":"private"}`),
	}}})
	view, err := recorder.GetView(context.Background(), types.Document{ID: documentID})
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
	if len(view.Stages) != 1 || view.Stages[0].ErrorCode != "TASK_FAILED" {
		t.Fatalf("sanitized processing view = %#v", view)
	}
}
