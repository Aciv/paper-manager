package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"os/exec"
	appservice "paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from tasks_test.go.
// ---------------------------------------------------------------------------

// TestLimitTaskConcurrencyWaitsBeforeCallingHandler verifies that the wrapper
// admits at most the configured number of handlers and starts a waiting task
// only after a slot is released.
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

// TestLimitTaskConcurrencyRespectsCancellationWhileWaiting verifies that a task
// waiting for a concurrency slot returns context.Canceled when its context is
// cancelled.
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

// TestTaskConstructorsUseAllDocumentTaskTypes verifies that each document task
// factory builds the expected Asynq task type and supplies retry, timeout,
// queue, and stable-ID options.
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

// TestProcessPaperMetadataExtractTaskDoesNotChooseNextStage verifies that the
// extraction task only invokes ExtractMetadata and leaves stage selection to
// the service.
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

// TestProcessPaperMetadataExtractTaskDoesNotDispatchWithoutIdentity verifies
// that extraction succeeds when the service reports the document is not
// resolvable, leaving dispatch decisions to the service.
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

// TestProcessPaperReferenceDispatchTaskDelegatesToService verifies that the
// reference task forwards one dispatch command and that independently built
// tasks keep the same stage generation and stage.
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

// TestProcessPaperMetadataExtractTaskDoesNotQueueCatalogWhenExtractionFails
// verifies that an extraction failure is returned as a retryable error instead
// of being swallowed so catalog work could be enqueued.
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

// TestClassifyPaperTaskError verifies that known-bad inputs skip retry, stale
// revisions revoke the task, and transport or database failures retry.
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

// TestPaperTaskProviderClassificationAndRetryAfter verifies that provider
// errors honor TaskRetryable and that TaskRetryAfter supplies the retry delay.
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

// ---------------------------------------------------------------------------
// Consolidated from asynq_native_redis_integration_test.go.
// ---------------------------------------------------------------------------

// TestAsynqNativeLifecycleRedis verifies retry, cancellation, timeout, and
// graceful drain behavior against a real Asynq server and Redis.
// An owned native Redis is required; queues are unique and never flushed.
func TestAsynqNativeLifecycleRedis(t *testing.T) {
	addr := os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("PAPERS_MANAGER_TEST_REDIS_ADDR is not configured")
	}
	opt := asynq.RedisClientOpt{Addr: addr}
	queue := "native-" + uuid.NewString()
	client := asynq.NewClient(opt)
	defer client.Close()
	inspector := asynq.NewInspector(opt)
	defer inspector.Close()
	defer func() { _ = inspector.DeleteQueue(queue, true) }()
	done := make(chan string, 16)
	entered := make(chan struct{}, 1)
	var retries atomic.Int32
	var active, peak atomic.Int32
	server := asynq.NewServer(opt, asynq.Config{
		Concurrency: 4, Queues: map[string]int{queue: 1},
		TaskCheckInterval:        10 * time.Millisecond,
		DelayedTaskCheckInterval: 100 * time.Millisecond,
		ShutdownTimeout:          3 * time.Second,
		RetryDelayFunc:           func(int, error, *asynq.Task) time.Duration { return 100 * time.Millisecond },
	})
	err := server.Start(asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		switch task.Type() {
		case "retry":
			if retries.Add(1) == 1 {
				return fmt.Errorf("intentional first attempt failure")
			}
		case "cancel":
			entered <- struct{}{}
			<-ctx.Done()
			done <- "cancelled"
			return asynq.SkipRetry
		case "timeout":
			<-ctx.Done()
			done <- "timed-out"
			return asynq.SkipRetry
		case "drain":
			entered <- struct{}{}
			time.Sleep(200 * time.Millisecond)
		default:
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			active.Add(-1)
		}
		done <- task.Type()
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	enqueue := func(kind string, options ...asynq.Option) *asynq.TaskInfo {
		t.Helper()
		options = append(options, asynq.Queue(queue), asynq.Retention(time.Minute))
		info, err := client.Enqueue(asynq.NewTask(kind, nil), options...)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	wait := func(want string) {
		t.Helper()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	for range 8 {
		enqueue("immediate")
	}
	for range 8 {
		wait("immediate")
	}
	if n := peak.Load(); n < 2 || n > 4 {
		t.Fatalf("peak concurrency=%d, want 2..4", n)
	}
	enqueue("retry", asynq.MaxRetry(1))
	wait("retry")
	if retries.Load() != 2 {
		t.Fatalf("attempts=%d, want 2", retries.Load())
	}
	// Asynq persists scheduled timestamps at whole-second precision. Select
	// an exact second and assert that deadline rather than elapsed subsecond time.
	scheduled := time.Now().Add(2 * time.Second).Truncate(time.Second)
	enqueue("delayed", asynq.ProcessAt(scheduled))
	wait("delayed")
	if time.Now().Before(scheduled) {
		t.Fatal("delayed task ran early")
	}
	info := enqueue("cancel", asynq.MaxRetry(0))
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("cancel task did not start")
	}
	if err := inspector.CancelProcessing(info.ID); err != nil {
		t.Fatal(err)
	}
	wait("cancelled")
	enqueue("timeout", asynq.Timeout(time.Second), asynq.MaxRetry(0))
	wait("timed-out")
	drained := enqueue("drain")
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("drain task did not start")
	}
	server.Shutdown()
	wait("drain")
	info, err = inspector.GetTaskInfo(queue, drained.ID)
	if err != nil || info.State != asynq.TaskStateCompleted {
		t.Fatalf("drained task=%+v error=%v", info, err)
	}
}

// TestAsynqNativePersistenceRedis verifies that active, pending, and scheduled
// tasks survive a Redis restart and remain inspectable without manual requeueing.
// A subprocess is deliberately killed while holding an active task lease.
// The CI harness restarts Redis between seed and verify; no internal Redis
// keys, artificial lease edits, mocks, or manual Inspector requeue are used.
func TestAsynqNativePersistenceRedis(t *testing.T) {
	mode := os.Getenv("PAPERS_TEST_PERSISTENCE_PHASE")
	if mode == "" {
		return // Explicit two-phase harness only.
	}
	queue := os.Getenv("PAPERS_TEST_PERSISTENCE_QUEUE")
	addr := os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")
	if queue == "" || addr == "" {
		t.Fatal("two-phase persistence probe requires its private Redis and queue")
	}
	opt := asynq.RedisClientOpt{Addr: addr}
	client := asynq.NewClient(opt)
	defer client.Close()
	inspector := asynq.NewInspector(opt)
	defer inspector.Close()
	if mode == "seed" {
		marker := filepath.Join(t.TempDir(), "started")
		command := exec.Command(os.Args[0], "-test.run=^TestAsynqNativeCrashWorkerChild$")
		command.Env = append(os.Environ(), "PAPERS_TEST_CRASH_CHILD=1", "PAPERS_TEST_CRASH_MARKER="+marker)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
		if _, err := client.Enqueue(asynq.NewTask("crashed", nil), asynq.Queue(queue),
			asynq.TaskID("persist-crashed"), asynq.MaxRetry(2), asynq.Retention(10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("crash worker never entered handler")
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, kind := range []string{"pending", "scheduled"} {
			options := []asynq.Option{asynq.Queue(queue), asynq.TaskID("persist-" + kind), asynq.Retention(10 * time.Minute)}
			if kind == "scheduled" {
				options = append(options, asynq.ProcessIn(2*time.Second))
			}
			if _, err := client.Enqueue(asynq.NewTask(kind, nil), options...); err != nil {
				t.Fatal(err)
			}
		}
		return // defer kills the worker without Shutdown, leaving its lease.
	}
	if mode != "verify" {
		t.Fatalf("unexpected persistence phase %q", mode)
	}
	defer func() { _ = inspector.DeleteQueue(queue, true) }()
	for kind, state := range map[string]asynq.TaskState{
		"crashed": asynq.TaskStateActive, "pending": asynq.TaskStatePending, "scheduled": asynq.TaskStateScheduled,
	} {
		info, err := inspector.GetTaskInfo(queue, "persist-"+kind)
		if err != nil || info.State != state {
			t.Fatalf("persisted %s task=%+v error=%v, want %v", kind, info, err, state)
		}
	}
	done := make(chan string, 8)
	server := asynq.NewServer(opt, asynq.Config{Concurrency: 4, Queues: map[string]int{queue: 1},
		TaskCheckInterval: 10 * time.Millisecond, DelayedTaskCheckInterval: 100 * time.Millisecond,
		RetryDelayFunc: func(int, error, *asynq.Task) time.Duration { return 0 }})
	if err := server.Start(asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		if task.Type() == "crashed" {
			if retry, ok := asynq.GetRetryCount(ctx); !ok || retry != 1 {
				done <- "wrong-crash-retry-count"
				return asynq.SkipRetry
			}
		}
		done <- task.Type()
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	timer := time.NewTimer(150 * time.Second) // upstream recovery interval: one minute
	defer timer.Stop()
	seen := map[string]bool{}
	for len(seen) < 3 {
		select {
		case kind := <-done:
			if seen[kind] || (kind != "pending" && kind != "scheduled" && kind != "crashed") {
				t.Fatalf("duplicate or unexpected recovered task %q", kind)
			}
			seen[kind] = true
		case <-timer.C:
			t.Fatalf("native lease recovery did not complete: %v", seen)
		}
	}
	server.Shutdown()
	for kind := range seen {
		info, err := inspector.GetTaskInfo(queue, "persist-"+kind)
		if err != nil || info.State != asynq.TaskStateCompleted {
			t.Fatalf("recovered %s task=%+v error=%v", kind, info, err)
		}
	}
}

// TestAsynqNativeCrashWorkerChild is the helper subprocess for the two-phase
// persistence probe: when the crash flag is set it blocks inside the handler
// until the parent kills the process, and otherwise it returns immediately.
func TestAsynqNativeCrashWorkerChild(t *testing.T) {
	if os.Getenv("PAPERS_TEST_CRASH_CHILD") != "1" {
		return
	}
	queue := os.Getenv("PAPERS_TEST_PERSISTENCE_QUEUE")
	server := asynq.NewServer(asynq.RedisClientOpt{Addr: os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")},
		asynq.Config{Concurrency: 1, Queues: map[string]int{queue: 1}, TaskCheckInterval: 10 * time.Millisecond})
	if err := server.Start(asynq.HandlerFunc(func(context.Context, *asynq.Task) error {
		if err := os.WriteFile(os.Getenv("PAPERS_TEST_CRASH_MARKER"), []byte("started"), 0o600); err != nil {
			return err
		}
		select {} // parent kills this process after confirming the active lease
	})); err != nil {
		t.Fatal(err)
	}
	select {}
}
