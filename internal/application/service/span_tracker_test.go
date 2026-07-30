package service

import (
	"context"
	"testing"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// spanRepositoryStub records the narrow persistence interactions needed to
// prove SpanTracker preserves parent links and can refresh a live operation.
type spanRepositoryStub struct {
	stored          types.DocumentProcessingSpan
	heartbeatSpanID uuid.UUID
	rows            []types.DocumentProcessingSpan
}

func (s *spanRepositoryStub) UpsertRunning(_ context.Context, span types.DocumentProcessingSpan) (types.DocumentProcessingSpan, error) {
	if span.ID == uuid.Nil {
		span.ID = uuid.New()
	}
	s.stored = span
	return span, nil
}
func (s *spanRepositoryStub) Heartbeat(_ context.Context, id uuid.UUID) error {
	s.heartbeatSpanID = id
	return nil
}
func (*spanRepositoryStub) Finish(context.Context, uuid.UUID, types.ProcessingSpanStatus, string, string) error {
	return nil
}
func (*spanRepositoryStub) FinishAttempt(context.Context, uuid.UUID, int, types.ProcessingSpanStatus, string, string) error {
	return nil
}
func (s *spanRepositoryStub) ListLatestAttempt(context.Context, uuid.UUID) ([]types.DocumentProcessingSpan, error) {
	return append([]types.DocumentProcessingSpan(nil), s.rows...), nil
}
func (*spanRepositoryStub) HasActive(context.Context, uuid.UUID, ...types.ProcessingSpanKind) (bool, error) {
	return false, nil
}
func (*spanRepositoryStub) CancelActive(context.Context, uuid.UUID, string, string) error { return nil }
func (*spanRepositoryStub) HasActiveScope(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID) (bool, error) {
	return false, nil
}
func (*spanRepositoryStub) ListActiveScopeIDs(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope) ([]types.ProcessingSpanScopeID, error) {
	return nil, nil
}
func (*spanRepositoryStub) CancelActiveScope(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID, string, string) error {
	return nil
}
func (*spanRepositoryStub) CancelActiveScopePrefix(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID, string, string) error {
	return nil
}
func (*spanRepositoryStub) IsScopeCancelled(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID) (bool, error) {
	return false, nil
}
func (*spanRepositoryStub) ForceCancelScope(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID, string, string) error {
	return nil
}

var _ interfaces.DocumentProcessingSpanRepository = (*spanRepositoryStub)(nil)

func TestSpanTrackerBeginChildPersistsParentAndHeartbeats(t *testing.T) {
	repo := &spanRepositoryStub{}
	tracker := NewSpanTracker(repo)
	parentID, documentID := uuid.New(), uuid.New()
	spanID, err := tracker.BeginChild(
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

func TestSpanTrackerViewUsesRecognitionAttemptNotPaperRetryAttempt(t *testing.T) {
	documentID := uuid.New()
	tracker := NewSpanTracker(&spanRepositoryStub{rows: []types.DocumentProcessingSpan{
		{DocumentID: documentID, Attempt: 2, Kind: types.ProcessingSpanRecognition},
		{DocumentID: documentID, Attempt: 99, Kind: types.ProcessingSpanMetadataExtract},
	}})
	view, err := tracker.GetView(context.Background(), types.Document{ID: documentID})
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
