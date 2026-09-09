package limit

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

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
