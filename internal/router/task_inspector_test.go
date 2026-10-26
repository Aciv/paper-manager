package router

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"os"
	"paperMaster/internal/types"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from task_inspector_test.go.
// ---------------------------------------------------------------------------

// TestTaskInspectorAdaptersShareOneAsynqInspector verifies that document and
// result inspectors created from one shared inspector reuse the same backend.
func TestTaskInspectorAdaptersShareOneAsynqInspector(t *testing.T) {
	backend := &asynq.Inspector{}
	shared := &SharedAsynqInspector{inspector: backend}
	documentInspector, ok := NewTaskInspector(shared).(*asynqTaskInspector)
	if !ok || documentInspector.inspector != backend {
		t.Fatalf("document inspector did not reuse shared backend: %#v", documentInspector)
	}
	resultInspector, ok := NewTaskResultInspector(shared).(*SharedAsynqInspector)
	if !ok || resultInspector != shared || resultInspector.inspector != backend {
		t.Fatalf("result inspector did not reuse shared backend: %#v", resultInspector)
	}
}

// TestMatchesDocumentUsesDocumentIDPayloadField verifies that probes select
// tasks only by the document_id payload and expected task type, ignoring the
// legacy knowledge_id field and unrelated types.
func TestMatchesDocumentUsesDocumentIDPayloadField(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	if !matchesDocument(types.TypeDocumentRecognize, []byte(`{"document_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("document task with matching document_id was not selected")
	}
	if matchesDocument(types.TypeDocumentRecognize, []byte(`{"knowledge_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("legacy knowledge_id payload was unexpectedly selected")
	}
	if matchesDocument("unrelated:task", []byte(`{"document_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("unrelated task type was unexpectedly selected")
	}
	if !matchesDocument(
		"paper:metadata_extract",
		[]byte(`{"document_id":"`+documentID+`"}`),
		documentID,
		"paper:metadata_extract",
	) {
		t.Fatal("paper extraction task was not selected")
	}
	if matchesDocument(
		"paper:source_query_enqueue",
		[]byte(`{"document_id":"`+documentID+`"}`),
		documentID,
		"paper:metadata_extract",
	) {
		t.Fatal("task-type-specific probe selected the wrong paper task")
	}
}

// TestRecordSnapshotTaskCapturesStageAndReferenceEnvelopes verifies that
// document and source query-apply tasks populate stage fences, reference tasks
// populate reference fences only with a nonzero generation, and unrelated tasks
// are ignored.
func TestRecordSnapshotTaskCapturesStageAndReferenceEnvelopes(t *testing.T) {
	snapshot := types.NewQueuedWorkSnapshot()
	recordSnapshotTask(&snapshot, types.TypeDocumentRecognize,
		[]byte(`{"document_id":"doc-1","stage":"markdown","generation":2}`))
	recordSnapshotTask(&snapshot, types.TypePaperQueryApply,
		[]byte(`{"kind":"source","document_id":"doc-2","stage":"source_lookup","generation":3}`))
	recordSnapshotTask(&snapshot, types.TypePaperQueryApply,
		[]byte(`{"kind":"reference","reference_id":"ref-1","generation":4}`))
	recordSnapshotTask(&snapshot, types.TypePaperQueryApply,
		[]byte(`{"kind":"reference","reference_id":"ref-2","generation":0}`))
	recordSnapshotTask(&snapshot, "unrelated:task",
		[]byte(`{"document_id":"doc-3","stage":"markdown","generation":1}`))

	if !snapshot.HasStage("doc-1", types.DocumentStageMarkdown, 2) {
		t.Fatal("document stage task was not captured")
	}
	if !snapshot.HasStage("doc-2", types.DocumentStageSourceLookup, 3) {
		t.Fatal("source query-apply task was not captured")
	}
	if !snapshot.HasReference("ref-1", 4) {
		t.Fatal("reference query-apply task was not captured")
	}
	if snapshot.HasReference("ref-2", 0) {
		t.Fatal("zero-generation reference task was captured")
	}
	if snapshot.HasStage("doc-3", types.DocumentStageMarkdown, 1) {
		t.Fatal("unrelated task was captured")
	}
}

// TestMatchesDocumentStageMatchesSourceQueryApplyResult verifies that a source
// query-apply result matches only the source_lookup stage and never the
// reference_lookup stage.
func TestMatchesDocumentStageMatchesSourceQueryApplyResult(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	payload := []byte(`{"kind":"source","document_id":"` + documentID + `","stage":"source_lookup","generation":3}`)
	if !matchesDocumentStage(types.TypePaperQueryApply, payload, documentID, types.DocumentStageSourceLookup, 3) {
		t.Fatal("source query-apply result was not matched to its stage")
	}
	if matchesDocumentStage(types.TypePaperQueryApply, payload, documentID, types.DocumentStageReferenceLookup, 3) {
		t.Fatal("source result matched the wrong stage")
	}
	referencePayload := []byte(`{"kind":"reference","document_id":"` + documentID + `","reference_id":"ref","stage":"reference_lookup","generation":3}`)
	if matchesDocumentStage(types.TypePaperQueryApply, referencePayload, documentID, types.DocumentStageReferenceLookup, 3) {
		t.Fatal("reference result must not count as a source_lookup task")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from task_inspector_match_test.go.
// ---------------------------------------------------------------------------

// TestMatchesDocumentStage verifies that matching requires the exact task type,
// document ID, stage, and generation.
func TestMatchesDocumentStage(t *testing.T) {
	payload, err := json.Marshal(DocumentIDProbe{DocumentID: "doc-a", Stage: types.DocumentStageMarkdown, Generation: 4})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		taskType   string
		documentID string
		stage      types.DocumentStage
		generation int64
		want       bool
	}{
		{"exact", types.TypeDocumentRecognize, "doc-a", types.DocumentStageMarkdown, 4, true},
		{"new generation", types.TypeDocumentRecognize, "doc-a", types.DocumentStageMarkdown, 5, false},
		{"other stage", types.TypeDocumentRecognize, "doc-a", types.DocumentStageMetadataExtraction, 4, false},
		{"other document", types.TypeDocumentRecognize, "doc-b", types.DocumentStageMarkdown, 4, false},
		{"unsupported task", "other", "doc-a", types.DocumentStageMarkdown, 4, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesDocumentStage(tc.taskType, payload, tc.documentID, tc.stage, tc.generation); got != tc.want {
				t.Fatalf("matchesDocumentStage() = %t, want %t", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consolidated from task_inspector_redis_integration_test.go.
// ---------------------------------------------------------------------------

// TestHasQueuedQueryApplyForReferenceRedis is opt-in because it needs Redis.
// Run with:
//
//	PAPERS_MANAGER_TEST_REDIS_ADDR=127.0.0.1:6380 go test ./internal/router \
//	  -run '^TestHasQueuedQueryApplyForReferenceRedis$' -v
func TestHasQueuedQueryApplyForReferenceRedis(t *testing.T) {
	addr := os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("PAPERS_MANAGER_TEST_REDIS_ADDR is not configured")
	}
	previous, hadPrevious := os.LookupEnv("REDIS_ADDR")
	if err := os.Setenv("REDIS_ADDR", addr); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("REDIS_ADDR", previous)
		} else {
			_ = os.Unsetenv("REDIS_ADDR")
		}
	}()

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: addr})
	defer client.Close()
	inspector := NewAsynqInspector()
	defer inspector.Close()
	probe := NewAsynqTaskInspector(inspector)

	referenceID := uuid.NewString()
	taskID := "query-apply-probe-" + referenceID
	payload := fmt.Sprintf(
		`{"kind":"reference","document_id":%q,"reference_id":%q,"generation":3}`,
		uuid.NewString(), referenceID,
	)
	info, err := client.EnqueueContext(context.Background(),
		asynq.NewTask(types.TypePaperQueryApply, []byte(payload)),
		asynq.Queue(types.QueueLow), asynq.TaskID(taskID),
	)
	if err != nil {
		t.Fatalf("enqueue probe task: %v", err)
	}
	defer func() {
		_ = inspector.DeleteTask(types.QueueLow, info.ID)
	}()

	found, err := probe.HasQueuedQueryApplyForReference(context.Background(), referenceID, 3)
	if err != nil {
		t.Fatalf("HasQueuedQueryApplyForReference: %v", err)
	}
	if !found {
		t.Fatal("pending reference query_apply task was not found")
	}
	found, err = probe.HasQueuedQueryApplyForReference(context.Background(), referenceID, 4)
	if err != nil {
		t.Fatalf("HasQueuedQueryApplyForReference(wrong generation): %v", err)
	}
	if found {
		t.Fatal("query_apply task matched the wrong generation")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from task_inspector_snapshot_redis_integration_test.go.
// ---------------------------------------------------------------------------

// TestSnapshotQueuedWorkBoundedRedis seeds 1,000 stage tasks and verifies the
// snapshot stays complete and bounded. It uses Redis DB 15 so it never touches
// application queues, and flushes only that DB on cleanup.
//
//	PAPERS_MANAGER_TEST_REDIS_ADDR=127.0.0.1:6379 go test ./internal/router \
//	  -run '^TestSnapshotQueuedWorkBoundedRedis$' -v
func TestSnapshotQueuedWorkBoundedRedis(t *testing.T) {
	addr := os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("PAPERS_MANAGER_TEST_REDIS_ADDR is not configured")
	}
	previousAddr, hadAddr := os.LookupEnv("REDIS_ADDR")
	previousDB, hadDB := os.LookupEnv("REDIS_DB")
	if err := os.Setenv("REDIS_ADDR", addr); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("REDIS_DB", "15"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadAddr {
			_ = os.Setenv("REDIS_ADDR", previousAddr)
		} else {
			_ = os.Unsetenv("REDIS_ADDR")
		}
		if hadDB {
			_ = os.Setenv("REDIS_DB", previousDB)
		} else {
			_ = os.Unsetenv("REDIS_DB")
		}
	})

	redisClient := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	t.Cleanup(func() {
		_ = redisClient.FlushDB(context.Background()).Err()
		_ = redisClient.Close()
	})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect test Redis: %v", err)
	}

	client := asynq.NewClient(getAsynqRedisClientOpt())
	t.Cleanup(func() { _ = client.Close() })
	const taskCount = 1000
	documentIDs := make([]string, 0, taskCount)
	for index := 0; index < taskCount; index++ {
		documentID := uuid.NewString()
		documentIDs = append(documentIDs, documentID)
		payload := fmt.Sprintf(
			`{"document_id":%q,"stage":"markdown","generation":1}`, documentID,
		)
		if _, err := client.EnqueueContext(context.Background(),
			asynq.NewTask(types.TypeDocumentRecognize, []byte(payload)),
			asynq.Queue(types.QueueDefault),
			asynq.TaskID("snapshot-scale-"+documentID),
		); err != nil {
			t.Fatalf("enqueue task %d: %v", index, err)
		}
	}

	inspector := NewAsynqInspector()
	t.Cleanup(func() { inspector.Close() })
	probe := NewAsynqTaskInspector(inspector)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := time.Now()
	snapshot, err := probe.SnapshotQueuedWork(ctx)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("SnapshotQueuedWork() error = %v", err)
	}
	if !snapshot.Complete {
		t.Fatal("snapshot was incomplete for 1,000 queued tasks")
	}
	if len(snapshot.StageTasks) < taskCount {
		t.Fatalf("snapshot stage tasks = %d, want at least %d", len(snapshot.StageTasks), taskCount)
	}
	if !snapshot.HasStage(documentIDs[0], types.DocumentStageMarkdown, 1) {
		t.Fatal("first enqueued task is missing from the snapshot")
	}
	if snapshot.HasStage(documentIDs[0], types.DocumentStageMarkdown, 2) {
		t.Fatal("snapshot matched the wrong generation")
	}
	t.Logf("snapshot over 1,000 queued tasks: %s", elapsed)
}
