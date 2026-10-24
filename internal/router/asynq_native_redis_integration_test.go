package router

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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
