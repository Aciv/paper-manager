package router

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	appservice "paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLimitTaskConcurrencyWaitsBeforeCallingHandler(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	results := make(chan error, 3)
	var running atomic.Int32
	var maximum atomic.Int32

	limited := limitTaskConcurrency(2, func(context.Context, *asynq.Task) error {
		current := running.Add(1)
		defer running.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		return nil
	})

	for range 3 {
		go func() {
			results <- limited(context.Background(), asynq.NewTask("test", nil))
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for admitted task")
		}
	}
	select {
	case <-started:
		t.Fatal("third task entered before a concurrency slot was released")
	case <-time.After(50 * time.Millisecond):
	}

	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("third task did not enter after a concurrency slot was released")
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 3 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent handlers = %d, want 2", got)
	}
}

func TestLimitTaskConcurrencyRespectsCancellationWhileWaiting(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	firstResult := make(chan error, 1)
	limited := limitTaskConcurrency(1, func(context.Context, *asynq.Task) error {
		close(started)
		<-release
		return nil
	})
	go func() {
		firstResult <- limited(context.Background(), asynq.NewTask("test", nil))
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limited(ctx, asynq.NewTask("test", nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting task error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
}

type paperInfoServiceStub struct {
	resolvable       bool
	extractErr       error
	resolveErr       error
	dispatchErr      error
	extractCommands  []types.PaperMetadataExtractCommand
	resolveCommands  []types.PaperSourceQueryEnqueueCommand
	dispatchCommands []types.PaperReferenceQueryEnqueueCommand
}

func (stub *paperInfoServiceStub) ExtractMetadata(
	_ context.Context,
	command types.PaperMetadataExtractCommand,
) (bool, error) {
	stub.extractCommands = append(stub.extractCommands, command)
	return stub.resolvable, stub.extractErr
}

func (stub *paperInfoServiceStub) PaperSourceQueryEnqueue(
	_ context.Context,
	command types.PaperSourceQueryEnqueueCommand,
) error {
	stub.resolveCommands = append(stub.resolveCommands, command)
	return stub.resolveErr
}

func (stub *paperInfoServiceStub) ReferenceQueryEnqueue(
	_ context.Context,
	command types.PaperReferenceQueryEnqueueCommand,
) error {
	stub.dispatchCommands = append(stub.dispatchCommands, command)
	return stub.dispatchErr
}

func (*paperInfoServiceStub) QueryApply(
	context.Context,
	types.PaperQueryApplyCommand,
) error {
	return nil
}

func (*paperInfoServiceStub) RetryExtraction(context.Context, string) error {
	return nil
}

func (*paperInfoServiceStub) RetryCatalogResolution(context.Context, string) error {
	return nil
}
func (*paperInfoServiceStub) RetryReferenceResolution(context.Context, string) error {
	return nil
}
func (*paperInfoServiceStub) Cancel(context.Context, string) error {
	return nil
}

type documentTaskClientStub struct {
	tasks       []*asynq.Task
	optionCount []int
	err         error
}

func (stub *documentTaskClientStub) EnqueueContext(
	_ context.Context,
	task *asynq.Task,
	options ...asynq.Option,
) (*asynq.TaskInfo, error) {
	stub.tasks = append(stub.tasks, task)
	stub.optionCount = append(stub.optionCount, len(options))
	return nil, stub.err
}

func TestTaskConstructorsUseAllDocumentTaskTypes(t *testing.T) {
	client := &documentTaskClientStub{}
	cfg := config.Config{
		PaperExtractionTaskTimeout:  30 * time.Second,
		PaperExtractionTaskMaxRetry: 2,
		PaperCatalogTaskTimeout:     time.Minute,
		PaperCatalogTaskMaxRetry:    4,
	}
	documentID := uuid.NewString()
	documentTask, documentOptions, err := appservice.NewDocumentRecognitionTask(
		types.DocumentProcessPayload{DocumentID: documentID, Stage: types.DocumentStageMarkdown, Generation: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	extractionTask, extractionOptions, err := appservice.NewPaperMetadataExtractTask(
		types.PaperMetadataExtractPayload{
			DocumentID: documentID, Stage: types.DocumentStageMetadataExtraction, Generation: 1,
		},
		cfg.PaperExtractionTaskTimeout,
		cfg.PaperExtractionTaskMaxRetry,
	)
	if err != nil {
		t.Fatal(err)
	}
	catalogTask, catalogOptions, err := appservice.NewPaperSourceQueryEnqueueTask(
		types.PaperSourceQueryEnqueuePayload{DocumentID: documentID, Stage: types.DocumentStageSourceLookup, Generation: 1},
		cfg.PaperCatalogTaskTimeout,
		cfg.PaperCatalogTaskMaxRetry,
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatchTask, dispatchOptions, err := appservice.NewPaperReferenceQueryEnqueueTask(
		types.PaperReferenceQueryEnqueuePayload{DocumentID: documentID, Stage: types.DocumentStageReferenceLookup, Generation: 1},
		cfg.PaperCatalogTaskTimeout,
		cfg.PaperCatalogTaskMaxRetry,
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, built := range []struct {
		task    *asynq.Task
		options []asynq.Option
	}{
		{documentTask, documentOptions},
		{extractionTask, extractionOptions},
		{catalogTask, catalogOptions},
		{dispatchTask, dispatchOptions},
	} {
		if _, err := client.EnqueueContext(
			context.Background(),
			built.task,
			built.options...,
		); err != nil {
			t.Fatalf("enqueue task %d: %v", index, err)
		}
	}
	if len(client.tasks) != 4 {
		t.Fatalf("enqueued task count = %d, want 4", len(client.tasks))
	}
	if client.tasks[0].Type() != types.TypeDocumentRecognize ||
		client.tasks[1].Type() != types.TypePaperMetadataExtract ||
		client.tasks[2].Type() != types.TypePaperSourceQueryEnqueue ||
		client.tasks[3].Type() != types.TypePaperReferenceQueryEnqueue {
		t.Fatalf("task types = %q, %q, %q, %q",
			client.tasks[0].Type(),
			client.tasks[1].Type(),
			client.tasks[2].Type(),
			client.tasks[3].Type())
	}
	if client.optionCount[0] != 4 ||
		client.optionCount[1] != 4 ||
		client.optionCount[2] != 4 ||
		client.optionCount[3] != 4 {
		t.Fatalf("option counts = %#v, want all task factories to provide retry, timeout, queue, and stable IDs", client.optionCount)
	}
}

func TestProcessPaperMetadataExtractTaskDoesNotChooseNextStage(t *testing.T) {
	documentID := uuid.NewString()
	payload := types.PaperMetadataExtractPayload{
		RequestID:  "request-1",
		DocumentID: documentID,
		Stage:      types.DocumentStageMetadataExtraction, Generation: 1,
	}
	task := asynq.NewTask(
		types.TypePaperMetadataExtract,
		mustJSON(t, payload),
	)
	info := &paperInfoServiceStub{resolvable: true}
	if err := processPaperMetadataExtractTask(
		context.Background(),
		task,
		info,
	); err != nil {
		t.Fatalf("processPaperMetadataExtractTask() error = %v", err)
	}
	if len(info.extractCommands) != 1 {
		t.Fatalf("extract command count = %d, want 1", len(info.extractCommands))
	}
}

func TestProcessPaperMetadataExtractTaskDoesNotDispatchWithoutIdentity(t *testing.T) {
	payload := types.PaperMetadataExtractPayload{
		DocumentID: uuid.NewString(), Stage: types.DocumentStageMetadataExtraction, Generation: 1,
	}
	info := &paperInfoServiceStub{resolvable: false}
	err := processPaperMetadataExtractTask(
		context.Background(),
		asynq.NewTask(types.TypePaperMetadataExtract, mustJSON(t, payload)),
		info,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessPaperReferenceDispatchTaskDelegatesToService(t *testing.T) {
	documentID := uuid.New()
	task, _, err := appservice.NewPaperReferenceQueryEnqueueTask(
		types.PaperReferenceQueryEnqueuePayload{DocumentID: documentID.String(), Stage: types.DocumentStageReferenceLookup, Generation: 1},
		time.Minute,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	info := &paperInfoServiceStub{}
	if err := processPaperReferenceQueryEnqueueTask(context.Background(), task, info); err != nil {
		t.Fatal(err)
	}
	if len(info.dispatchCommands) != 1 || info.dispatchCommands[0].DocumentID != documentID {
		t.Fatalf("dispatch commands = %#v", info.dispatchCommands)
	}
	second, _, err := appservice.NewPaperReferenceQueryEnqueueTask(
		types.PaperReferenceQueryEnqueuePayload{DocumentID: documentID.String(), Stage: types.DocumentStageReferenceLookup, Generation: 1},
		time.Minute,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	var firstPayload, secondPayload types.PaperReferenceQueryEnqueuePayload
	if err := json.Unmarshal(task.Payload(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Payload(), &secondPayload); err != nil {
		t.Fatal(err)
	}
	if firstPayload.Generation != secondPayload.Generation || firstPayload.Stage != secondPayload.Stage {
		t.Fatalf("reference stage fences differ: %#v %#v", firstPayload, secondPayload)
	}
}

func TestProcessPaperMetadataExtractTaskDoesNotQueueCatalogWhenExtractionFails(t *testing.T) {
	payload := types.PaperMetadataExtractPayload{
		DocumentID: uuid.NewString(),
	}
	info := &paperInfoServiceStub{
		resolvable: true,
		extractErr: errors.New("extractor unavailable"),
	}
	err := processPaperMetadataExtractTask(
		context.Background(),
		asynq.NewTask(types.TypePaperMetadataExtract, mustJSON(t, payload)),
		info,
	)
	if err == nil {
		t.Fatal("processPaperMetadataExtractTask() error = nil, want retryable extraction error")
	}
}

func TestClassifyPaperTaskError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "invalid payload skips retry",
			err:  types.ErrInvalidPaperInfoTaskPayload,
			want: asynq.SkipRetry,
		},
		{
			name: "no match skips retry",
			err:  appservice.ErrPaperCatalogNoMatch,
			want: asynq.SkipRetry,
		},
		{
			name: "catalog unavailable skips retry",
			err:  appservice.ErrPaperInfoCatalogUnavailable,
			want: asynq.SkipRetry,
		},
		{
			name: "stale revision revokes",
			err:  appservice.ErrStalePaperInfo,
			want: asynq.RevokeTask,
		},
		{
			name: "grpc invalid argument skips retry",
			err:  status.Error(codes.InvalidArgument, "bad request"),
			want: asynq.SkipRetry,
		},
		{
			name: "grpc unavailable retries",
			err:  status.Error(codes.Unavailable, "offline"),
		},
		{
			name: "database errors retry",
			err:  errors.New("database unavailable"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyPaperTaskError(test.err)
			if test.want == nil {
				if errors.Is(got, asynq.SkipRetry) ||
					errors.Is(got, asynq.RevokeTask) {
					t.Fatalf("classified error = %v, want retry", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("classified error = %v, want %v", got, test.want)
			}
		})
	}
}

type taskRetryAfterError struct {
	retryable bool
	delay     time.Duration
}

func (err taskRetryAfterError) Error() string                 { return "provider failed" }
func (err taskRetryAfterError) TaskRetryable() bool           { return err.retryable }
func (err taskRetryAfterError) TaskRetryAfter() time.Duration { return err.delay }

func TestPaperTaskProviderClassificationAndRetryAfter(t *testing.T) {
	nonRetryable := taskRetryAfterError{}
	if got := classifyPaperTaskError(nonRetryable); !errors.Is(got, asynq.SkipRetry) {
		t.Fatalf("non-retryable provider error = %v, want SkipRetry", got)
	}
	retryable := taskRetryAfterError{retryable: true, delay: 7 * time.Second}
	if got := classifyPaperTaskError(retryable); errors.Is(got, asynq.SkipRetry) {
		t.Fatalf("retryable provider error = %v, want normal retry", got)
	}
	if got := paperTaskRetryAfter(retryable); got != 7*time.Second {
		t.Fatalf("retry delay = %s, want 7s", got)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
