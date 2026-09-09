// Package limit provides the shared process-wide and distributed lease
// primitive used by outbound integrations. Business adapters define the
// namespaces, resource keys, and policies; this package only coordinates
// concurrency, spacing, leases, and cooldowns.
package limit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	defaultLeaseTTL = 2 * time.Minute
	contentionPoll  = 100 * time.Millisecond
)

// Dimension is one independently constrained resource. All dimensions in an
// Acquire call are checked and leased atomically.
type Dimension struct {
	Namespace   string
	Key         string
	Concurrency int
	Spacing     time.Duration
}

// Request describes one hierarchical lease, for example a global download
// slot plus a per-host slot.
type Request struct {
	Dimensions []Dimension
	LeaseTTL   time.Duration
}

// ReleaseFunc releases every dimension acquired by one request. It is safe to
// call more than once.
type ReleaseFunc func()

// Limiter atomically leases one or more resource dimensions and stores shared
// not-before timestamps for server-directed cooldowns.
type Limiter interface {
	Acquire(ctx context.Context, request Request) (ReleaseFunc, error)
	Cooldown(ctx context.Context, dimension Dimension, delay time.Duration) error
}

type state struct {
	next   time.Time
	active int
}

// Engine uses Redis when configured and otherwise keeps equivalent state in
// memory for lite mode and focused tests.
type Engine struct {
	mu     sync.Mutex
	states map[string]state
	redis  *redis.Client
}

// NewEngine creates the shared limiter backend used by all business adapters.
func NewEngine() *Engine {
	engine := NewMemoryEngine()
	if addr := strings.TrimSpace(os.Getenv("REDIS_ADDR")); addr != "" {
		db, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("REDIS_DB")))
		engine.redis = redis.NewClient(&redis.Options{
			Addr: addr, Username: strings.TrimSpace(os.Getenv("REDIS_USERNAME")),
			Password: os.Getenv("REDIS_PASSWORD"), DB: db,
		})
	}
	return engine
}

// NewMemoryEngine creates an isolated in-process limiter.
func NewMemoryEngine() *Engine {
	return &Engine{states: make(map[string]state)}
}

func (e *Engine) Acquire(ctx context.Context, request Request) (ReleaseFunc, error) {
	dimensions, err := normalize(request.Dimensions)
	if err != nil {
		return nil, err
	}
	leaseTTL := request.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}
	if e == nil {
		return nil, errors.New("limiter is not configured")
	}
	if e.redis != nil {
		return e.acquireRedis(ctx, dimensions, leaseTTL)
	}
	return e.acquireMemory(ctx, dimensions)
}

func (e *Engine) Cooldown(ctx context.Context, dimension Dimension, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	dimensions, err := normalize([]Dimension{dimension})
	if err != nil {
		return err
	}
	if e == nil {
		return errors.New("limiter is not configured")
	}
	key := resourceKey(dimensions[0])
	if e.redis != nil {
		now := time.Now()
		return e.redis.Eval(ctx, cooldownScript, []string{nextKey(key)}, now.Add(delay).UnixMilli(), now.UnixMilli()).Err()
	}
	until := time.Now().Add(delay)
	e.mu.Lock()
	current := e.states[key]
	if current.next.Before(until) {
		current.next = until
	}
	e.states[key] = current
	e.mu.Unlock()
	return nil
}

func normalize(input []Dimension) ([]Dimension, error) {
	if len(input) == 0 {
		return nil, errors.New("at least one limiter dimension is required")
	}
	dimensions := append([]Dimension(nil), input...)
	seen := make(map[string]struct{}, len(dimensions))
	for index := range dimensions {
		dimensions[index].Namespace = strings.ToLower(strings.TrimSpace(dimensions[index].Namespace))
		dimensions[index].Key = strings.ToLower(strings.TrimSpace(dimensions[index].Key))
		if dimensions[index].Namespace == "" || dimensions[index].Key == "" {
			return nil, errors.New("limiter namespace and key are required")
		}
		if dimensions[index].Concurrency < 1 {
			return nil, fmt.Errorf("limiter concurrency for %s/%s must be positive", dimensions[index].Namespace, dimensions[index].Key)
		}
		key := resourceKey(dimensions[index])
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("limiter dimension %s is duplicated", key)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(dimensions, func(i, j int) bool { return resourceKey(dimensions[i]) < resourceKey(dimensions[j]) })
	return dimensions, nil
}

func resourceKey(dimension Dimension) string {
	return dimension.Namespace + ":" + dimension.Key
}

func (e *Engine) acquireMemory(ctx context.Context, dimensions []Dimension) (ReleaseFunc, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		wait := time.Duration(0)
		e.mu.Lock()
		available := true
		for _, dimension := range dimensions {
			current := e.states[resourceKey(dimension)]
			switch {
			case current.next.After(now):
				available = false
				if delay := time.Until(current.next); delay > wait {
					wait = delay
				}
			case current.active >= dimension.Concurrency:
				available = false
				if wait < contentionPoll {
					wait = contentionPoll
				}
			}
		}
		if available {
			for _, dimension := range dimensions {
				key := resourceKey(dimension)
				current := e.states[key]
				current.active++
				if next := now.Add(dimension.Spacing); current.next.Before(next) {
					current.next = next
				}
				e.states[key] = current
			}
			e.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					e.mu.Lock()
					defer e.mu.Unlock()
					for _, dimension := range dimensions {
						key := resourceKey(dimension)
						current := e.states[key]
						if current.active > 0 {
							current.active--
						}
						e.states[key] = current
					}
				})
			}, nil
		}
		e.mu.Unlock()
		if err := waitContext(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// KEYS contains next/active pairs. Active leases are individual sorted-set
// members, so one abandoned lease expires independently without a shared
// counter being prolonged by unrelated acquisitions. ARGV contains now, lease
// TTL, lease token, followed by spacing/concurrency pairs.
const acquireScript = `
local now = tonumber(ARGV[1])
local lease_ms = tonumber(ARGV[2])
local token = ARGV[3]
local wait_ms = 0
for i = 1, #KEYS, 2 do
  local arg = 4 + ((i - 1) / 2) * 2
  local max_active = tonumber(ARGV[arg + 1])
  local next_at = tonumber(redis.call('GET', KEYS[i]) or '0')
  if next_at > now and next_at - now > wait_ms then wait_ms = next_at - now end
  redis.call('ZREMRANGEBYSCORE', KEYS[i + 1], '-inf', now)
  local active = redis.call('ZCARD', KEYS[i + 1])
  if active >= max_active and wait_ms < 100 then wait_ms = 100 end
end
if wait_ms > 0 then return {0, wait_ms} end
for i = 1, #KEYS, 2 do
  local arg = 4 + ((i - 1) / 2) * 2
  local spacing = tonumber(ARGV[arg])
  if spacing > 0 then
    redis.call('SET', KEYS[i], now + spacing, 'PX', math.max(spacing * 2, 1000))
  end
  redis.call('ZADD', KEYS[i + 1], now + lease_ms, token)
  redis.call('PEXPIRE', KEYS[i + 1], lease_ms + 1000)
end
return {1, 0}`

const releaseScript = `
for i = 1, #KEYS do
  redis.call('ZREM', KEYS[i], ARGV[1])
  if redis.call('ZCARD', KEYS[i]) == 0 then redis.call('DEL', KEYS[i]) end
end
return 1`

const cooldownScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local proposed = tonumber(ARGV[1])
if current < proposed then
  redis.call('SET', KEYS[1], proposed, 'PX', math.max(proposed - tonumber(ARGV[2]) + 1000, 1000))
end
return 1`

func nextKey(resource string) string   { return "papers:limit:" + resource + ":next" }
func activeKey(resource string) string { return "papers:limit:" + resource + ":active" }

func (e *Engine) acquireRedis(ctx context.Context, dimensions []Dimension, leaseTTL time.Duration) (ReleaseFunc, error) {
	keys := make([]string, 0, len(dimensions)*2)
	args := make([]interface{}, 0, 3+len(dimensions)*2)
	leaseToken := uuid.NewString()
	args = append(args, time.Now().UnixMilli(), leaseTTL.Milliseconds(), leaseToken)
	for _, dimension := range dimensions {
		resource := resourceKey(dimension)
		keys = append(keys, nextKey(resource), activeKey(resource))
		args = append(args, dimension.Spacing.Milliseconds(), dimension.Concurrency)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		args[0] = time.Now().UnixMilli()
		result, err := e.redis.Eval(ctx, acquireScript, keys, args...).Int64Slice()
		if err != nil {
			return nil, fmt.Errorf("distributed limiter unavailable: %w", err)
		}
		if len(result) != 2 {
			return nil, errors.New("distributed limiter returned an invalid response")
		}
		if result[0] == 1 {
			activeKeys := make([]string, 0, len(dimensions))
			for _, dimension := range dimensions {
				activeKeys = append(activeKeys, activeKey(resourceKey(dimension)))
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					_, _ = e.redis.Eval(context.Background(), releaseScript, activeKeys, leaseToken).Result()
				})
			}, nil
		}
		if result[0] != 0 {
			return nil, errors.New("distributed limiter rejected an invalid response")
		}
		wait := contentionPoll
		if result[1] > 0 {
			wait = time.Duration(result[1]) * time.Millisecond
		}
		if err := waitContext(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		delay = time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ Limiter = (*Engine)(nil)
