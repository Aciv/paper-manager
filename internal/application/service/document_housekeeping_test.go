package service

import (
	"context"
	"errors"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type documentQueueProbeStub struct {
	queued map[string]bool
	errs   map[string]error
}

func (s *documentQueueProbeStub) HasQueuedTaskForStage(_ context.Context, documentID string, _ types.DocumentStage, _ int64) (bool, error) {
	return s.queued[documentID], s.errs[documentID]
}

func (*documentQueueProbeStub) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (s *documentQueueProbeStub) SnapshotQueuedWork(context.Context) (types.QueuedWorkSnapshot, error) {
	snapshot := types.NewQueuedWorkSnapshot()
	for _, err := range s.errs {
		if err != nil {
			return snapshot, err
		}
	}
	for documentID, queued := range s.queued {
		if queued {
			snapshot.StageTasks[types.QueuedStageKey(documentID, types.DocumentStageMarkdown, 1)] = struct{}{}
		}
	}
	return snapshot, nil
}

func (*documentQueueProbeStub) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (*documentQueueProbeStub) CancelTasksForStage(context.Context, string, types.DocumentStage, int64) (int, int, error) {
	return 0, 0, nil
}
func (s *documentQueueProbeStub) HasQueuedTasksForDocument(_ context.Context, documentID string) (bool, error) {
	return s.queued[documentID], s.errs[documentID]
}

func (s *documentQueueProbeStub) HasQueuedTaskTypeForDocument(
	_ context.Context,
	documentID string,
	_ string,
) (bool, error) {
	return s.queued[documentID], s.errs[documentID]
}

func TestDocumentHousekeepingStageProbeStub(t *testing.T) {
	queuedDocument := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	orphanedDocument := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	probeFailureDocument := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service := &DocumentHousekeepingService{inspector: &documentQueueProbeStub{
		queued: map[string]bool{queuedDocument.ID.String(): true},
		errs:   map[string]error{probeFailureDocument.ID.String(): errors.New("redis unavailable")},
	}}

	queued, err := service.inspector.HasQueuedTaskForStage(context.Background(), queuedDocument.ID.String(), types.DocumentStageMarkdown, 1)
	if err != nil || !queued {
		t.Fatalf("queued stage = %t, error=%v", queued, err)
	}
	queued, err = service.inspector.HasQueuedTaskForStage(context.Background(), orphanedDocument.ID.String(), types.DocumentStageMarkdown, 1)
	if err != nil || queued {
		t.Fatalf("orphaned stage = %t, error=%v", queued, err)
	}
	if _, err = service.inspector.HasQueuedTaskForStage(context.Background(), probeFailureDocument.ID.String(), types.DocumentStageMarkdown, 1); err == nil {
		t.Fatal("queue probe error was lost")
	}
}

func TestDocumentStageColumnMapping(t *testing.T) {
	cases := map[types.DocumentStage]string{
		types.DocumentStageMarkdown:           "markdown_status",
		types.DocumentStageMetadataExtraction: "metadata_extraction_status",
		types.DocumentStageSourceLookup:       "source_lookup_status",
		types.DocumentStageReferenceLookup:    "reference_lookup_status",
	}
	for stage, want := range cases {
		if got := stageStateColumn(stage); got != want {
			t.Fatalf("stageStateColumn(%s) = %q, want %q", stage, got, want)
		}
	}
	if got := stageStateColumn("unknown"); got != "" {
		t.Fatalf("unknown stage column = %q", got)
	}
}
