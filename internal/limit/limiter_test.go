package limit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireDoesNotHoldPartialHierarchy(t *testing.T) {
	limiter := NewMemoryEngine()
	download := func(host string) Request {
		return Request{Dimensions: []Dimension{
			{Namespace: "download", Key: "global", Concurrency: 2},
			{Namespace: "download", Key: host, Concurrency: 1},
		}}
	}
	releaseA, err := limiter.Acquire(context.Background(), download("a.example"))
	if err != nil {
		t.Fatalf("Acquire(a) error = %v", err)
	}
	defer releaseA()

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiterDone := make(chan error, 1)
	go func() {
		release, acquireErr := limiter.Acquire(waiterCtx, download("a.example"))
		if release != nil {
			release()
		}
		waiterDone <- acquireErr
	}()
	time.Sleep(20 * time.Millisecond)

	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelAcquire()
	releaseB, err := limiter.Acquire(acquireCtx, download("b.example"))
	if err != nil {
		t.Fatalf("Acquire(b) blocked by partial global lease: %v", err)
	}
	releaseB()
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Acquire(a) error = %v, want context.Canceled", err)
	}
}

func TestCooldownExtendsNotBeforeAndHonorsContext(t *testing.T) {
	limiter := NewMemoryEngine()
	dimension := Dimension{Namespace: "provider", Key: "crossref", Concurrency: 1}
	if err := limiter.Cooldown(context.Background(), dimension, 80*time.Millisecond); err != nil {
		t.Fatalf("Cooldown() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx, Request{Dimensions: []Dimension{dimension}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v, want context deadline", err)
	}
	time.Sleep(70 * time.Millisecond)
	release, err := limiter.Acquire(context.Background(), Request{Dimensions: []Dimension{dimension}})
	if err != nil {
		t.Fatalf("Acquire() after cooldown error = %v", err)
	}
	release()
}

func TestReleaseIsIdempotent(t *testing.T) {
	limiter := NewMemoryEngine()
	request := Request{Dimensions: []Dimension{{Namespace: "test", Key: "one", Concurrency: 1}}}
	release, err := limiter.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	release()
	release()
	second, err := limiter.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("Acquire() after repeated release error = %v", err)
	}
	second()
}
