package router

import (
	"context"
	"fmt"
	"os"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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
