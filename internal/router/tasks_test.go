package router

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

type paperInfoServiceStub struct {
	resolvable      bool
	extractErr      error
	resolveErr      error
	extractCommands []types.PaperMetadataExtractCommand
	resolveCommands []types.PaperCatalogResolveCommand
}

func (stub *paperInfoServiceStub) ExtractMetadata(
	_ context.Context,
	command types.PaperMetadataExtractCommand,
) (bool, error) {
	stub.extractCommands = append(stub.extractCommands, command)
	return stub.resolvable, stub.extractErr
}

func (stub *paperInfoServiceStub) ResolveMetadata(
	_ context.Context,
	command types.PaperCatalogResolveCommand,
) error {
	stub.resolveCommands = append(stub.resolveCommands, command)
	return stub.resolveErr
}

func (*paperInfoServiceStub) GetStatus(context.Context, string) (types.PaperInfoStatus, error) {
	return types.PaperInfoStatus{}, nil
}

func (*paperInfoServiceStub) RetryExtraction(context.Context, string) (types.PaperInfoStatus, error) {
	return types.PaperInfoStatus{}, nil
}

func (*paperInfoServiceStub) RetryCatalogResolution(context.Context, string) (types.PaperInfoStatus, error) {
	return types.PaperInfoStatus{}, nil
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
	revision := strings.Repeat("b", 64)
	documentTask, documentOptions, err := appservice.NewDocumentProcessTask(
		types.DocumentProcessPayload{DocumentID: documentID},
	)
	if err != nil {
		t.Fatal(err)
	}
	extractionTask, extractionOptions, err := appservice.NewPaperMetadataExtractTask(
		types.PaperMetadataExtractPayload{
			DocumentID:     documentID,
			MarkdownSHA256: revision,
		},
		cfg.PaperExtractionTaskTimeout,
		cfg.PaperExtractionTaskMaxRetry,
	)
	if err != nil {
		t.Fatal(err)
	}
	catalogTask, catalogOptions, err := appservice.NewPaperCatalogResolveTask(
		types.PaperCatalogResolvePayload{DocumentID: documentID},
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
	} {
		if _, err := client.EnqueueContext(
			context.Background(),
			built.task,
			built.options...,
		); err != nil {
			t.Fatalf("enqueue task %d: %v", index, err)
		}
	}
	if len(client.tasks) != 3 {
		t.Fatalf("enqueued task count = %d, want 3", len(client.tasks))
	}
	if client.tasks[0].Type() != types.TypeDocumentProcess ||
		client.tasks[1].Type() != types.TypePaperMetadataExtract ||
		client.tasks[2].Type() != types.TypePaperCatalogResolve {
		t.Fatalf("task types = %q, %q, %q",
			client.tasks[0].Type(),
			client.tasks[1].Type(),
			client.tasks[2].Type())
	}
	if client.optionCount[0] != 3 ||
		client.optionCount[1] != 4 ||
		client.optionCount[2] != 4 {
		t.Fatalf("option counts = %#v, want process=3 and paper stages=4", client.optionCount)
	}
}

func TestProcessPaperMetadataExtractTaskChainsCatalogForTitleOrDOI(t *testing.T) {
	documentID := uuid.NewString()
	revision := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	payload := types.PaperMetadataExtractPayload{
		RequestID:      "request-1",
		DocumentID:     documentID,
		MarkdownSHA256: revision,
		Attempt:        2,
	}
	task := asynq.NewTask(
		types.TypePaperMetadataExtract,
		mustJSON(t, payload),
	)
	info := &paperInfoServiceStub{resolvable: true}
	enqueuer := &documentTaskClientStub{}
	if err := processPaperMetadataExtractTask(
		context.Background(),
		task,
		info,
		enqueuer,
		config.Config{},
	); err != nil {
		t.Fatalf("processPaperMetadataExtractTask() error = %v", err)
	}
	if len(info.extractCommands) != 1 {
		t.Fatalf("extract command count = %d, want 1", len(info.extractCommands))
	}
	if len(enqueuer.tasks) != 1 {
		t.Fatalf("catalog enqueue count = %d, want 1", len(enqueuer.tasks))
	}
	var got types.PaperCatalogResolvePayload
	if err := json.Unmarshal(enqueuer.tasks[0].Payload(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DocumentID != documentID ||
		got.MarkdownSHA256 != revision ||
		got.Attempt != 2 {
		t.Fatalf("catalog payload = %#v", got)
	}
}

func TestProcessPaperMetadataExtractTaskSkipsCatalogWithoutIdentity(t *testing.T) {
	payload := types.PaperMetadataExtractPayload{
		DocumentID:     uuid.NewString(),
		MarkdownSHA256: strings.Repeat("a", 64),
	}
	info := &paperInfoServiceStub{resolvable: false}
	enqueuer := &documentTaskClientStub{}
	err := processPaperMetadataExtractTask(
		context.Background(),
		asynq.NewTask(types.TypePaperMetadataExtract, mustJSON(t, payload)),
		info,
		enqueuer,
		config.Config{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(enqueuer.tasks) != 0 {
		t.Fatalf("catalog tasks = %#v, want none", enqueuer.tasks)
	}
}

func TestProcessPaperMetadataExtractTaskDoesNotQueueCatalogWhenExtractionFails(t *testing.T) {
	payload := types.PaperMetadataExtractPayload{
		DocumentID:     uuid.NewString(),
		MarkdownSHA256: strings.Repeat("a", 64),
	}
	info := &paperInfoServiceStub{
		resolvable: true,
		extractErr: errors.New("extractor unavailable"),
	}
	enqueuer := &documentTaskClientStub{}
	err := processPaperMetadataExtractTask(
		context.Background(),
		asynq.NewTask(types.TypePaperMetadataExtract, mustJSON(t, payload)),
		info,
		enqueuer,
		config.Config{},
	)
	if err == nil {
		t.Fatal("processPaperMetadataExtractTask() error = nil, want retryable extraction error")
	}
	if len(enqueuer.tasks) != 0 {
		t.Fatalf("catalog queued before extraction success: %#v", enqueuer.tasks)
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
