package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			asynq.TaskID("persist-crashed"), asynq.MaxRetry(2), asynq.Retention(time.Minute)); err != nil {
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
			options := []asynq.Option{asynq.Queue(queue), asynq.TaskID("persist-" + kind), asynq.Retention(time.Minute)}
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
