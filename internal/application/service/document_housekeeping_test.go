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

func (*documentQueueProbeStub) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}

func (*documentQueueProbeStub) CancelReferenceTasksForRun(context.Context, string, string) (int, int, error) {
	return 0, 0, nil
}
func (*documentQueueProbeStub) CancelReferenceTasksForDocuments(context.Context, []string) (int, int, error) {
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

func TestDocumentHousekeepingFilterOutQueued(t *testing.T) {
	queuedDocument := types.Document{ID: uuid.New(), Status: types.DocumentStatusQueued}
	orphanedDocument := types.Document{ID: uuid.New(), Status: types.DocumentStatusProcessing}
	probeFailureDocument := types.Document{ID: uuid.New(), Status: types.DocumentStatusProcessing}
	service := &DocumentHousekeepingService{inspector: &documentQueueProbeStub{
		queued: map[string]bool{queuedDocument.ID.String(): true},
		errs:   map[string]error{probeFailureDocument.ID.String(): errors.New("redis unavailable")},
	}}

	kept, skipped := service.filterOutQueued(context.Background(), []types.Document{
		queuedDocument, orphanedDocument, probeFailureDocument,
	})
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(kept) != 2 || kept[0].ID != orphanedDocument.ID || kept[1].ID != probeFailureDocument.ID {
		t.Fatalf("kept = %#v, want orphaned and probe-failure documents", kept)
	}
}
