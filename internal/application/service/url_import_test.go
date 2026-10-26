package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type urlImportDocuments struct {
	interfaces.DocumentService
	create func(context.Context, types.DocumentURLImportCommand) (*types.Document, error)
}

func (d *urlImportDocuments) CreateDocumentFromURL(
	ctx context.Context, command types.DocumentURLImportCommand,
) (*types.Document, error) {
	return d.create(ctx, command)
}

type urlImportBases struct {
	interfaces.DocumentBaseService
	added []string
}

func (b *urlImportBases) GetBase(context.Context, string) (*types.DocumentBase, error) {
	return &types.DocumentBase{}, nil
}
func (b *urlImportBases) AddDocumentToBase(_ context.Context, _, id string) error {
	b.added = append(b.added, id)
	return nil
}

type urlImportQueue struct{ records map[string]types.TaskRecord }

func (q *urlImportQueue) EnqueueContext(
	ctx context.Context, task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var payload types.URLImportPayload
	_ = json.Unmarshal(task.Payload(), &payload)
	id := urlImportTaskPrefix + payload.RequestID
	if _, exists := q.records[id]; exists {
		return nil, asynq.ErrTaskIDConflict
	}
	q.records[id] = types.TaskRecord{ID: id, Type: task.Type(), State: "pending", Payload: task.Payload()}
	return &asynq.TaskInfo{ID: id}, nil
}
func (q *urlImportQueue) GetTask(_ context.Context, _, id string) (types.TaskRecord, error) {
	record, found := q.records[id]
	if !found {
		return types.TaskRecord{}, types.ErrTaskNotFound
	}
	return record, nil
}

type progressWriter struct{ write func([]byte) error }

func (w progressWriter) Write(p []byte) (int, error) { return len(p), w.write(p) }

func TestURLImportReturnsBeforeDownloadAndSurvivesClientCancellation(t *testing.T) {
	queue := &urlImportQueue{records: map[string]types.TaskRecord{}}
	bases := &urlImportBases{}
	calls := 0
	documents := &urlImportDocuments{create: func(
		context.Context, types.DocumentURLImportCommand,
	) (*types.Document, error) {
		calls++
		return &types.Document{ID: uuid.New()}, nil
	}}
	service := NewURLImportService(documents, bases, queue, queue, config.Config{}).(*urlImportService)
	requestID := uuid.NewString()
	commands := []types.DocumentURLImportCommand{{URL: "https://example.org/a.pdf"}}
	ctx, cancel := context.WithCancel(context.Background())
	job, err := service.EnqueueURLs(ctx, commands, uuid.NewString(), requestID)
	if err != nil || calls != 0 || job.State != "queued" {
		t.Fatalf("enqueue performed work or failed: %#v, %v, calls=%d", job, err, calls)
	}
	cancel()
	var payload types.URLImportPayload
	_ = json.Unmarshal(queue.records[job.ID].Payload, &payload)
	writer := progressWriter{write: func(p []byte) error {
		record := queue.records[job.ID]
		record.State, record.Result = "active", append([]byte(nil), p...)
		queue.records[job.ID] = record
		return nil
	}}
	if err := service.processURLImport(context.Background(), payload, writer); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(bases.added) != 1 {
		t.Fatal("worker was cancelled by client or failed to attach imported document")
	}
	record := queue.records[job.ID]
	record.State = "completed"
	queue.records[job.ID] = record
	status, err := service.GetURLImportTask(context.Background(), job.ID)
	if err != nil || status.State != "succeeded" || status.Completed != 1 {
		t.Fatalf("result unavailable: %#v, %v", status, err)
	}
	if _, err := service.EnqueueURLs(context.Background(), commands, job.BaseID, requestID); err != nil {
		t.Fatalf("identical idempotent replay failed: %v", err)
	}
	commands[0].URL = "https://example.org/different.pdf"
	if _, err := service.EnqueueURLs(context.Background(), commands, job.BaseID, requestID); err == nil {
		t.Fatal("request ID accepted a different payload")
	}
}

func TestURLImportPublishesAndAttachesEachItemBeforeNextDownload(t *testing.T) {
	bases := &urlImportBases{}
	published := 0
	calls := 0
	documents := &urlImportDocuments{create: func(
		context.Context, types.DocumentURLImportCommand,
	) (*types.Document, error) {
		if len(bases.added) != calls || published != calls {
			t.Fatal("next download started before previous result/membership was persisted")
		}
		calls++
		if calls == 1 {
			return &types.Document{ID: uuid.New()}, types.ErrDuplicateFileHash
		}
		return &types.Document{ID: uuid.New()}, nil
	}}
	service := NewURLImportService(documents, bases, nil, nil, config.Config{}).(*urlImportService)
	payload := types.URLImportPayload{RequestID: uuid.NewString(), BaseID: uuid.NewString(),
		Commands: []types.DocumentURLImportCommand{{URL: "https://example.org/a.pdf"}, {URL: "https://example.org/b.pdf"}}}
	writer := progressWriter{write: func(p []byte) error {
		var result types.DocumentBatchResult
		if err := json.Unmarshal(p, &result); err != nil {
			return err
		}
		published = len(result.Items)
		return nil
	}}
	if err := service.processURLImport(context.Background(), payload, writer); err != nil || published != 2 {
		t.Fatalf("worker failed: %v", err)
	}
}

func TestURLImportBackgroundDeadlinePreservesPartialResults(t *testing.T) {
	calls := 0
	documents := &urlImportDocuments{create: func(
		ctx context.Context, _ types.DocumentURLImportCommand,
	) (*types.Document, error) {
		calls++
		if calls == 1 {
			return &types.Document{ID: uuid.New()}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	service := NewURLImportService(documents, nil, nil, nil,
		config.Config{BatchImportTimeout: 20 * time.Millisecond}).(*urlImportService)
	var data []byte
	writer := progressWriter{write: func(p []byte) error { data = append([]byte(nil), p...); return nil }}
	payload := types.URLImportPayload{RequestID: uuid.NewString(), Commands: make([]types.DocumentURLImportCommand, 3)}
	if err := service.processURLImport(context.Background(), payload, writer); err != nil {
		t.Fatal(err)
	}
	var result types.DocumentBatchResult
	_ = json.Unmarshal(data, &result)
	if !result.Stopped || len(result.Items) != 3 || result.Items[0].Document == nil || calls != 2 {
		t.Fatalf("partial results lost: %#v, calls=%d", result, calls)
	}
	if err := service.processURLImport(context.Background(), payload, progressWriter{write: func([]byte) error {
		return errors.New("Redis result write failed")
	}}); err == nil {
		t.Fatal("progress persistence failure was ignored")
	}
}

func TestURLImportStatusRejectsForeignTask(t *testing.T) {
	id := urlImportTaskPrefix + uuid.NewString()
	queue := &urlImportQueue{records: map[string]types.TaskRecord{id: {ID: id, Type: types.TypeBibliographyGenerate}}}
	service := NewURLImportService(nil, nil, nil, queue, config.Config{})
	if _, err := service.GetURLImportTask(context.Background(), id); !errors.Is(err, types.ErrTaskNotFound) {
		t.Fatalf("foreign task exposed: %v", err)
	}
}

type redisURLImportInspector struct{ inspector *asynq.Inspector }

func (i redisURLImportInspector) GetTask(_ context.Context, queue, id string) (types.TaskRecord, error) {
	info, err := i.inspector.GetTaskInfo(queue, id)
	if err != nil {
		return types.TaskRecord{}, err
	}
	return types.TaskRecord{
		ID: info.ID, Type: info.Type, State: info.State.String(), Payload: info.Payload, Result: info.Result,
	}, nil
}

func TestURLImportQueueRoundTrip(t *testing.T) {
	addr := os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("requires explicit test Redis")
	}
	// Database 14 is reserved by this test; never flush or consume the normal
	// queue database used by the rest of the integration suite.
	opt := asynq.RedisClientOpt{Addr: addr, DB: 14}
	client := asynq.NewClient(opt)
	defer client.Close()
	inspector := asynq.NewInspector(opt)
	defer inspector.Close()
	enteredSecond, releaseSecond := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSecond) }) }
	defer release()
	var calls atomic.Int32
	documents := &urlImportDocuments{create: func(
		ctx context.Context, _ types.DocumentURLImportCommand,
	) (*types.Document, error) {
		if calls.Add(1) == 2 {
			close(enteredSecond)
			select {
			case <-releaseSecond:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &types.Document{ID: uuid.New()}, nil
	}}
	bases := &urlImportBases{}
	service := NewURLImportService(documents, bases, client, redisURLImportInspector{inspector}, config.Config{})
	server := asynq.NewServer(opt, asynq.Config{Concurrency: 1, Queues: map[string]int{types.QueueDownload: 1},
		ShutdownTimeout: time.Second, TaskCheckInterval: 50 * time.Millisecond})
	if err := server.Start(asynq.HandlerFunc(service.ProcessURLImport)); err != nil {
		t.Fatal(err)
	}
	jobID := ""
	defer func() {
		release()
		server.Shutdown()
		if jobID != "" {
			_ = inspector.DeleteTask(types.QueueDownload, jobID)
		}
	}()
	requestCtx, cancel := context.WithCancel(context.Background())
	job, err := service.EnqueueURLs(requestCtx, []types.DocumentURLImportCommand{
		{URL: "https://example.org/a.pdf"}, {URL: "https://example.org/b.pdf"},
	}, uuid.NewString(), uuid.NewString())
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	jobID = job.ID
	select {
	case <-enteredSecond:
	case <-time.After(15 * time.Second):
		t.Fatal("queued job did not survive request cancellation")
	}
	partial, err := service.GetURLImportTask(context.Background(), job.ID)
	if err != nil || partial.State != "running" || partial.Completed != 1 || len(bases.added) != 1 {
		t.Fatalf("durable partial result/Base missing: %#v, %v", partial, err)
	}
	release()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.GetURLImportTask(context.Background(), job.ID)
		if err == nil && status.State == "succeeded" {
			freshInspector := asynq.NewInspector(opt)
			defer freshInspector.Close()
			fresh := NewURLImportService(nil, nil, nil, redisURLImportInspector{freshInspector}, config.Config{})
			persisted, err := fresh.GetURLImportTask(context.Background(), job.ID)
			if err != nil || persisted.Completed != 2 || calls.Load() != 2 {
				t.Fatalf("retained result lost: %#v, %v", persisted, err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("URL import did not complete")
}

func TestURLImportResumesSavedItemsWithoutDownloadingThemAgain(t *testing.T) {
	completedID := uuid.New()
	payload := types.URLImportPayload{RequestID: uuid.NewString(), Commands: []types.DocumentURLImportCommand{
		{URL: "https://example.org/a.pdf"}, {URL: "https://example.org/b.pdf"},
	}}
	previous := types.DocumentBatchResult{Items: []types.DocumentBatchItemResult{{Index: 0,
		URL: payload.Commands[0].URL, Status: types.DocumentImportItemAccepted,
		Document: &types.Document{ID: completedID}}}}
	documents := &urlImportDocuments{create: func(
		_ context.Context, command types.DocumentURLImportCommand,
	) (*types.Document, error) {
		if command.URL != payload.Commands[1].URL {
			t.Fatal("completed URL was downloaded again after worker restart")
		}
		return &types.Document{ID: uuid.New()}, nil
	}}
	service := NewURLImportService(documents, nil, nil, nil, config.Config{}).(*urlImportService)
	var result types.DocumentBatchResult
	writer := progressWriter{write: func(p []byte) error { return json.Unmarshal(p, &result) }}
	if err := service.processURLImport(context.Background(), payload, writer, previous); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Document.ID != completedID {
		t.Fatal("resumed batch lost its completed item")
	}
	previous.Items[0].URL = "https://example.org/other-batch.pdf"
	if err := service.processURLImport(context.Background(), payload, writer, previous); err == nil {
		t.Fatal("unrelated saved progress was accepted")
	}
}
