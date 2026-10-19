package router

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

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
