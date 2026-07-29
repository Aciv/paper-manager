package papercatalog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryProviderThrottleSharesSpacingCooldownAndLease(t *testing.T) {
	throttle := &memoryProviderThrottle{state: make(map[string]providerThrottleState)}
	release, err := throttle.Acquire(context.Background(), "crossref", time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = throttle.Acquire(context.Background(), "crossref", time.Second, 1)
	var limited *throttledProviderError
	if !errors.As(err, &limited) || limited.TaskRetryAfter() <= 0 || !limited.TaskRetryable() {
		t.Fatalf("second acquire = %v, want retryable shared cooldown", err)
	}
	if err := throttle.Cooldown(context.Background(), "crossref", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	release()
	_, err = throttle.Acquire(context.Background(), "crossref", time.Millisecond, 1)
	if !errors.As(err, &limited) || limited.TaskRetryAfter() < time.Second {
		t.Fatalf("cooldown acquire = %v, want global retry deadline", err)
	}
}

func TestReferenceCatalogLimiterBoundsGlobalAndPerDocumentLeases(t *testing.T) {
	limiter := &referenceCatalogLimiter{throttle: &memoryProviderThrottle{state: make(map[string]providerThrottleState)}, global: 1, perDocument: 1}
	release, err := limiter.AcquireReference(context.Background(), "source-a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := limiter.AcquireReference(context.Background(), "source-a"); err == nil {
		t.Fatal("same document acquired a second reference lease")
	}
	if _, err := limiter.AcquireReference(context.Background(), "source-b"); err == nil {
		t.Fatal("second document bypassed global reference lease")
	}
}
