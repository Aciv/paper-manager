package paperurl

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newMemoryDownloadLimiter(global, perHost int, spacing time.Duration) *downloadLimiter {
	return &downloadLimiter{
		global: global,
		max:    perHost,
		spacing: spacing,
		memory: &memoryDownloadLimiter{state: make(map[string]downloadHostState)},
	}
}

func TestDownloadLimiterEnforcesGlobalConcurrencyAcrossHosts(t *testing.T) {
	limiter := newMemoryDownloadLimiter(1, 1, 0)
	release, err := limiter.Acquire(context.Background(), "one.example")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx, "two.example"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v, want global concurrency timeout", err)
	}
	release()
	secondRelease, err := limiter.Acquire(context.Background(), "two.example")
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	secondRelease()
}

func TestDownloadLimiterHonorsHostCooldown(t *testing.T) {
	limiter := newMemoryDownloadLimiter(2, 1, 0)
	if err := limiter.Cooldown(context.Background(), "paper.example", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	release, err := limiter.Acquire(context.Background(), "paper.example")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("Acquire() waited %s, want cooldown wait", elapsed)
	}
}
