package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type bibliographyTaskEnqueuerStub struct {
	task *asynq.Task
	err  error
}

func (stub *bibliographyTaskEnqueuerStub) EnqueueContext(
	_ context.Context, task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	stub.task = task
	if stub.err != nil {
		return nil, stub.err
	}
	return &asynq.TaskInfo{ID: bibliographyTaskIDPrefix + uuid.NewString()}, nil
}

type taskResultInspectorStub struct {
	record types.TaskRecord
	err    error
	queue  string
}

func (stub *taskResultInspectorStub) GetTask(
	_ context.Context, queue, _ string,
) (types.TaskRecord, error) {
	stub.queue = queue
	return stub.record, stub.err
}

func TestEnqueueBaseBibliographyPublishesProjectTask(t *testing.T) {
	baseID := uuid.New()
	enqueuer := &bibliographyTaskEnqueuerStub{}
	service := newBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID}}},
		&doiCitationStub{}, enqueuer, nil, nil,
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1},
	)
	result, err := service.EnqueueBaseBibliography(context.Background(), baseID.String(), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != types.BibliographyTaskQueued || !validBibliographyTaskID(result.ID) {
		t.Fatalf("result = %#v", result)
	}
	if enqueuer.task == nil || enqueuer.task.Type() != types.TypeBibliographyGenerate {
		t.Fatalf("task = %#v", enqueuer.task)
	}
	var payload types.BibliographyTaskPayload
	if err := json.Unmarshal(enqueuer.task.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DocumentBaseID != baseID.String() || payload.RequestID != "request-1" || payload.Attempt < 1 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestGetBaseBibliographyTaskReturnsRetainedResult(t *testing.T) {
	baseID := uuid.New()
	runID := uuid.New()
	taskID := bibliographyTaskIDPrefix + runID.String()
	payload, _ := json.Marshal(types.BibliographyTaskPayload{
		DocumentBaseID: baseID.String(), RunID: runID.String(), Attempt: 1,
	})
	snapshot := types.BibliographySnapshot{BaseID: baseID, BaseName: "Base", BibTeX: "@misc{x}"}
	encoded, _ := json.Marshal(snapshot)
	inspector := &taskResultInspectorStub{record: types.TaskRecord{
		ID: taskID, Type: types.TypeBibliographyGenerate, State: "completed", Payload: payload, Result: encoded,
	}}
	service := newBibliographyService(&bibliographyRepositoryStub{}, &doiCitationStub{}, nil, inspector, nil, config.Config{})
	result, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != types.BibliographyTaskSucceeded || result.Snapshot == nil || result.Snapshot.BibTeX != "@misc{x}" {
		t.Fatalf("result = %#v", result)
	}
	if inspector.queue != types.QueueLow {
		t.Fatalf("inspector queue = %q", inspector.queue)
	}
	if _, err := service.GetBaseBibliographyTask(context.Background(), uuid.NewString(), taskID); err == nil {
		t.Fatal("expected cross-base task lookup rejection")
	}
	inspector.record.Type = types.TypeDocumentRecognize
	if _, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID); !errors.Is(err, types.ErrBibliographyTaskNotFound) {
		t.Fatalf("wrong task type error = %v", err)
	}
}

func TestGetBaseBibliographyTaskMapsGenericInspectorErrors(t *testing.T) {
	baseID := uuid.New()
	taskID := bibliographyTaskIDPrefix + uuid.NewString()
	inspector := &taskResultInspectorStub{err: types.ErrTaskInspectorUnavailable}
	service := newBibliographyService(&bibliographyRepositoryStub{}, &doiCitationStub{}, nil, inspector, nil, config.Config{})
	if _, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID); !errors.Is(err, types.ErrBibliographyTaskUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	inspector.err = types.ErrTaskNotFound
	if _, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID); !errors.Is(err, types.ErrBibliographyTaskNotFound) {
		t.Fatalf("not found error = %v", err)
	}
}

func TestProcessBaseBibliographyWritesResultAndFinishesDocumentSpans(t *testing.T) {
	baseID := uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	spans := &paperInfoSpanStub{}
	service := newBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{
			Base: types.DocumentBase{ID: baseID, Name: "Async"},
			Entries: []types.BibliographySource{
				{DocumentID: firstID, SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "First"}},
				{DocumentID: secondID, SourceState: types.DocumentSourceAvailable},
			},
		}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, errors: map[string]error{}, delays: map[string]time.Duration{}},
		nil, nil, spans,
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	payload := types.BibliographyTaskPayload{
		DocumentBaseID: baseID.String(), RunID: uuid.NewString(), Attempt: 7,
	}
	var output bytes.Buffer
	if err := service.processBaseBibliographyPayload(context.Background(), payload, &output); err != nil {
		t.Fatal(err)
	}
	var snapshot types.BibliographySnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Generated != 1 || snapshot.Failed != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(spans.starts) != 2 || len(spans.finishes) != 2 {
		t.Fatalf("span starts=%#v finishes=%#v", spans.starts, spans.finishes)
	}
	for _, started := range spans.starts {
		if started.kind != types.ProcessingSpanBibliographyExport || started.scope != types.ProcessingSpanScopeBibliographyExport || started.attempt != 7 {
			t.Fatalf("span start = %#v", started)
		}
	}
	statuses := map[types.ProcessingSpanStatus]int{}
	for _, finished := range spans.finishes {
		statuses[finished.status]++
	}
	if statuses[types.ProcessingSpanSucceeded] != 1 || statuses[types.ProcessingSpanFailed] != 1 {
		t.Fatalf("span statuses = %#v", statuses)
	}
}
