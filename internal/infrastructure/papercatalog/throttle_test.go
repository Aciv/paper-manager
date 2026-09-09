package papercatalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"paperMaster/internal/limit"
)

func TestMemoryProviderThrottleWaitsForSpacingCooldownAndLease(t *testing.T) {
	throttle := newProviderThrottle(limit.NewMemoryEngine())
	release, err := throttle.Acquire(context.Background(), "crossref", 20*time.Millisecond, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := throttle.Cooldown(context.Background(), "crossref", 35*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	acquired := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		release, err := throttle.Acquire(context.Background(), "crossref", 0, 1, time.Second)
		if err != nil {
			errs <- err
			return
		}
		acquired <- release
	}()

	select {
	case err := <-errs:
		t.Fatalf("waiting acquire returned error: %v", err)
	case <-acquired:
		t.Fatal("acquire succeeded before cooldown and active lease cleared")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case err := <-errs:
		t.Fatalf("waiting acquire returned error: %v", err)
	case waitingRelease := <-acquired:
		defer waitingRelease()
		if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
			t.Fatalf("acquire waited %s, want cooldown wait", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("acquire did not resume after cooldown and lease release")
	}
}

func TestMemoryProviderThrottleWaitHonorsCancellation(t *testing.T) {
	throttle := newProviderThrottle(limit.NewMemoryEngine())
	release, err := throttle.Acquire(context.Background(), "crossref", 0, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := throttle.Acquire(ctx, "crossref", 0, 1, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() error = %v, want context cancellation", err)
	}
}

func TestReferenceCatalogLimiterBoundsGlobalAndPerDocumentLeases(t *testing.T) {
	limiter := &referenceCatalogLimiter{limiter: limit.NewMemoryEngine(), global: 1, perDocument: 1}
	release, err := limiter.AcquireReference(context.Background(), "source-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.AcquireReference(ctx, "source-a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same document acquire error = %v, want context deadline", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.AcquireReference(ctx, "source-b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second document acquire error = %v, want context deadline", err)
	}
}
