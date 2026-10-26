package papercatalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"paperMaster/internal/limit"
)

// TestMemoryProviderThrottleWaitsForSpacingCooldownAndLease verifies that a
// concurrent acquire blocks until the host cooldown expires and the active slot is released.
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

// TestMemoryProviderThrottleWaitHonorsCancellation verifies that a blocked
// acquire aborts with context cancellation instead of waiting for a lease.
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
