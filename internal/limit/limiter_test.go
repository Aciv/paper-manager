package limit

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from limiter_test.go.
// ---------------------------------------------------------------------------

// TestAcquireDoesNotHoldPartialHierarchy verifies that a blocked acquisition
// does not retain already-granted parent dimensions, so another host can still
// use the remaining global slot.
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

// TestCooldownExtendsNotBeforeAndHonorsContext verifies that Cooldown blocks
// acquisition until it expires, respects caller deadlines, and admits work afterward.
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

// TestReleaseIsIdempotent verifies that calling a lease's release function twice
// does not corrupt capacity, allowing a subsequent acquisition.
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

// TestNewEngineUsesTLSAndTimeoutConfiguration verifies that the Redis-backed
// engine applies TLS settings and the configured operation timeouts to its client.
func TestNewEngineUsesTLSAndTimeoutConfiguration(t *testing.T) {
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("REDIS_USE_TLS", "true")
	t.Setenv("REDIS_OP_TIMEOUT_MS", "250")
	engine := NewEngine()
	t.Cleanup(func() { _ = engine.Close() })
	if engine.redis == nil {
		t.Fatal("redis client was not created")
	}
	options := engine.redis.Options()
	if options.TLSConfig == nil {
		t.Fatal("TLS config was not applied")
	}
	if options.ReadTimeout != 250*time.Millisecond || options.WriteTimeout != 500*time.Millisecond {
		t.Fatalf("timeouts = %s/%s, want 250ms/500ms", options.ReadTimeout, options.WriteTimeout)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from limiter_redis_integration_test.go.
// ---------------------------------------------------------------------------

// TestRedisLimiterAtomicHierarchyAndLeaseExpiry verifies against a real Redis
// that hierarchical acquisitions remain atomic across hosts and that an expired
// lease can be reclaimed; it skips when no test Redis is configured.
func TestRedisLimiterAtomicHierarchyAndLeaseExpiry(t *testing.T) {
	addr := os.Getenv("PAPERS_MANAGER_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("PAPERS_MANAGER_TEST_REDIS_ADDR is not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect test Redis: %v", err)
	}
	engine := NewMemoryEngine()
	engine.redis = client
	namespace := "integration-" + uuid.NewString()
	dimension := func(key string, concurrency int) Dimension {
		return Dimension{Namespace: namespace, Key: key, Concurrency: concurrency}
	}
	allDimensions := []Dimension{dimension("global", 2), dimension("host-a", 1), dimension("host-b", 1), dimension("expiry", 1)}
	t.Cleanup(func() {
		keys := make([]string, 0, len(allDimensions)*2)
		for _, item := range allDimensions {
			resource := resourceKey(item)
			keys = append(keys, nextKey(resource), activeKey(resource))
		}
		_ = client.Del(context.Background(), keys...).Err()
	})

	request := func(host string) Request {
		return Request{Dimensions: []Dimension{dimension("global", 2), dimension(host, 1)}}
	}
	releaseA, err := engine.Acquire(context.Background(), request("host-a"))
	if err != nil {
		t.Fatalf("Acquire(host-a) error = %v", err)
	}
	defer releaseA()

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		release, acquireErr := engine.Acquire(waiterCtx, request("host-a"))
		if release != nil {
			release()
		}
		waiterDone <- acquireErr
	}()
	time.Sleep(30 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	releaseB, err := engine.Acquire(ctx, request("host-b"))
	cancel()
	if err != nil {
		t.Fatalf("Acquire(host-b) blocked by a partial global lease: %v", err)
	}
	releaseB()
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting Acquire(host-a) error = %v, want context.Canceled", err)
	}

	expiryRequest := Request{
		Dimensions: []Dimension{dimension("expiry", 1)},
		LeaseTTL:   40 * time.Millisecond,
	}
	if _, err := engine.Acquire(context.Background(), expiryRequest); err != nil {
		t.Fatalf("Acquire(expiring lease) error = %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	expiryCtx, cancelExpiry := context.WithTimeout(context.Background(), 300*time.Millisecond)
	releaseExpired, err := engine.Acquire(expiryCtx, expiryRequest)
	cancelExpiry()
	if err != nil {
		t.Fatalf("Acquire() did not reclaim expired lease: %v", err)
	}
	releaseExpired()
}
