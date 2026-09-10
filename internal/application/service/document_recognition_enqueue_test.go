package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

func TestEnqueueDocumentRecognitionUsesStageGeneration(t *testing.T) {
	documentID := uuid.New()
	documents := &documentStatusRepoStub{document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable}}
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
	documents := &documentStatusRepoStub{document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable}}
	enqueuer := &documentTaskEnqueuerStub{enqueueErr: errors.New("queue unavailable")}
	service := &documentService{repo: documents, taskEnqueuer: enqueuer}
	if err := service.enqueueDocumentRecognition(context.Background(), documentID); err == nil {
		t.Fatal("enqueueDocumentRecognition() error = nil")
	}
	if len(enqueuer.tasks) != 1 {
		t.Fatalf("enqueued task attempts = %d, want 1", len(enqueuer.tasks))
	}
	if documents.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("document after queue failure = %#v", documents.document)
	}
}
