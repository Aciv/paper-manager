package papercatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/redis/go-redis/v9"
)

// providerThrottle is intentionally private to the infrastructure package.
// The application only sees providers; every provider registered from config
// is wrapped before it can issue HTTP calls.
type providerThrottle interface {
	Acquire(context.Context, string, time.Duration, int) (func(), error)
	// cool down only update the state, upate the next run time
	Cooldown(context.Context, string, time.Duration) error
}

type rateLimitedProvider struct {
	provider    interfaces.PaperCatalogProvider
	throttle    providerThrottle
	spacing     time.Duration
	concurrency int
}

func (p *rateLimitedProvider) Name() string { return p.provider.Name() }

func (p *rateLimitedProvider) Search(ctx context.Context, query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
	release, err := p.throttle.Acquire(ctx, p.Name(), p.spacing, p.concurrency)
	if err != nil {
		return nil, err
	}
	defer release()
	candidates, err := p.provider.Search(ctx, query)
	if err != nil {
		cooldown := time.Duration(0)
		var retryAfter interface{ TaskRetryAfter() time.Duration }
		if errors.As(err, &retryAfter) && retryAfter.TaskRetryAfter() > 0 {
			cooldown = retryAfter.TaskRetryAfter()
		}
		var providerErr *ProviderError
		if cooldown <= 0 && errors.As(err, &providerErr) && providerErr.StatusCode == 429 {
			// Public APIs frequently omit Retry-After. Keep this provider out
			// of the shared rotation for a conservative multiple of its normal
			// spacing instead of immediately feeding every waiting worker into
			// another 429.
			cooldown = 3 * p.spacing
			if cooldown < time.Second {
				cooldown = time.Second
			}
		}
		if cooldown > 0 {
			_ = p.throttle.Cooldown(context.WithoutCancel(ctx), p.Name(), cooldown)
		}
	}
	return candidates, err
}

// newProviderThrottle chooses Redis whenever the application has a configured
// Redis endpoint. Redis keys are shared by API worker processes, so a 429 from
// one worker changes the not-before time observed by every other worker. Lite
// mode falls back to a mutex-protected in-process implementation.
func newProviderThrottle() providerThrottle {
	if addr := strings.TrimSpace(os.Getenv("REDIS_ADDR")); addr != "" {
		db, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("REDIS_DB")))
		return &redisProviderThrottle{client: redis.NewClient(&redis.Options{
			Addr: addr, Username: strings.TrimSpace(os.Getenv("REDIS_USERNAME")),
			Password: os.Getenv("REDIS_PASSWORD"), DB: db,
		})}
	}
	return &memoryProviderThrottle{state: make(map[string]providerThrottleState)}
}

func wrapProvider(provider interfaces.PaperCatalogProvider, cfg config.Config, throttle providerThrottle) interfaces.PaperCatalogProvider {
	return &rateLimitedProvider{provider: provider, throttle: throttle,
		spacing: cfg.PaperCatalogSpacingFor(provider.Name()), concurrency: cfg.PaperCatalogConcurrencyFor(provider.Name())}
}

// throttledProviderError deliberately implements the queue's retry contracts
// for limiter infrastructure failures. Normal cooldown and lease contention
// are waited in-process by Acquire; they are not task failures.
type throttledProviderError struct {
	provider   string
	retryAfter time.Duration
	reason     string
}

func (e *throttledProviderError) Error() string {
	return fmt.Sprintf("%s catalog %s", e.provider, e.reason)
}
func (e *throttledProviderError) TaskRetryable() bool           { return true }
func (e *throttledProviderError) TaskRetryAfter() time.Duration { return e.retryAfter }

type providerThrottleState struct {
	next   time.Time
	active int
}
type memoryProviderThrottle struct {
	mu    sync.Mutex
	state map[string]providerThrottleState
}

func (t *memoryProviderThrottle) Acquire(ctx context.Context, provider string, spacing time.Duration, max int) (func(), error) {
	if max <= 0 {
		max = 1
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		t.mu.Lock()
		state := t.state[provider]
		var wait time.Duration
		switch {
		case state.next.After(now):
			wait = time.Until(state.next)
		case state.active >= max:
			// Releases do not have a waiter channel because state is also
			// shared by Cooldown. A short, context-aware poll avoids turning
			// temporary contention into an Asynq retry while still yielding.
			wait = 25 * time.Millisecond
		default:
			state.active++
			state.next = now.Add(spacing)
			t.state[provider] = state
			t.mu.Unlock()
			return func() {
				t.mu.Lock()
				state := t.state[provider]
				if state.active > 0 {
					state.active--
				}
				t.state[provider] = state
				t.mu.Unlock()
			}, nil
		}
		t.mu.Unlock()
		if err := waitForProviderThrottle(ctx, wait); err != nil {
			return nil, err
		}
	}
}
func (t *memoryProviderThrottle) Cooldown(_ context.Context, provider string, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	until := time.Now().Add(delay)
	t.mu.Lock()
	state := t.state[provider]
	if state.next.Before(until) {
		state.next = until
	}
	t.state[provider] = state
	t.mu.Unlock()
	return nil
}

type redisProviderThrottle struct{ client *redis.Client }

// []string{nextKey, activeKey}, now.UnixMilli(), spacing.Milliseconds(), max, lease.Milliseconds()
const acquireProviderLeaseScript = `
local next_key = KEYS[1]
local active_key = KEYS[2]
local now = tonumber(ARGV[1])
local spacing = tonumber(ARGV[2])
local max_active = tonumber(ARGV[3])
local lease_ms = tonumber(ARGV[4])
local next_at = tonumber(redis.call('GET', next_key) or '0')
if next_at > now then return {0, next_at - now} end
local active = tonumber(redis.call('GET', active_key) or '0')
if active >= max_active then return {-1, 250} end
redis.call('SET', next_key, now + spacing, 'PX', math.max(spacing * 2, 1000))
redis.call('INCR', active_key)
redis.call('PEXPIRE', active_key, lease_ms)
return {1, 0}`

const releaseProviderLeaseScript = `
local key = KEYS[1]
local active = tonumber(redis.call('GET', key) or '0')
if active <= 1 then redis.call('DEL', key) else redis.call('DECR', key) end
return 1`

// []string{nextKey}, now.Add(delay).UnixMilli(), now.UnixMilli()
const cooldownProviderScript = `
local key = KEYS[1]
local proposed = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local current = tonumber(redis.call('GET', key) or '0')
if current > proposed then return current end
redis.call('SET', key, proposed, 'PX', math.max(proposed - now + 1000, 1000))
return proposed`

func providerThrottleKeys(provider string) (string, string) {
	key := strings.ToLower(strings.TrimSpace(provider))
	return "papers:catalog:provider:" + key + ":next", "papers:catalog:provider:" + key + ":active"
}

func (t *redisProviderThrottle) Acquire(ctx context.Context, provider string, spacing time.Duration, max int) (func(), error) {
	if max <= 0 {
		max = 1
	}
	if spacing <= 0 {
		spacing = time.Millisecond
	}
	nextKey, activeKey := providerThrottleKeys(provider)
	lease := 2 * time.Minute
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		result, err := t.client.Eval(ctx, acquireProviderLeaseScript, []string{nextKey, activeKey}, now.UnixMilli(), spacing.Milliseconds(), max, lease.Milliseconds()).Int64Slice()
		if err != nil {
			return nil, &throttledProviderError{provider: provider, retryAfter: time.Second, reason: "global limiter unavailable"}
		}
		if len(result) != 2 {
			return nil, &throttledProviderError{provider: provider, retryAfter: time.Second, reason: "invalid global limiter response"}
		}
		if result[0] == 1 {
			return func() {
				_, _ = t.client.Eval(context.Background(), releaseProviderLeaseScript, []string{activeKey}).Result()
			}, nil
		}
		if result[0] != 0 && result[0] != -1 {
			return nil, &throttledProviderError{provider: provider, retryAfter: time.Second, reason: "rejected global limiter response"}
		}
		delay := 250 * time.Millisecond
		if result[1] > 0 {
			delay = time.Duration(result[1]) * time.Millisecond
		}
		if err := waitForProviderThrottle(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// waitForProviderThrottle waits without blocking a worker goroutine. It is
// shared by the Redis and Lite implementations so task cancellation and the
// Asynq timeout always interrupt limiter waits promptly.
func waitForProviderThrottle(ctx context.Context, delay time.Duration) error {
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

func (t *redisProviderThrottle) Cooldown(ctx context.Context, provider string, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	nextKey, _ := providerThrottleKeys(provider)
	now := time.Now()
	return t.client.Eval(ctx, cooldownProviderScript, []string{nextKey}, now.Add(delay).UnixMilli(), now.UnixMilli()).Err()
}

// referenceCatalogLimiter uses separate leases from provider throttles: it
// controls how many low-priority bibliography jobs run globally and for one
// citing document, while providerThrottle controls outbound API pressure.
type referenceCatalogLimiter struct {
	throttle    providerThrottle
	global      int
	perDocument int
}

func NewReferenceCatalogLimiter(cfg config.Config) interfaces.PaperReferenceCatalogLimiter {
	global, perDocument := cfg.PaperReferenceCatalogConcurrency, cfg.PaperReferenceCatalogPerDocumentConcurrency
	if global <= 0 {
		global = 2
	}
	if perDocument <= 0 {
		perDocument = 1
	}
	return &referenceCatalogLimiter{throttle: newProviderThrottle(), global: global, perDocument: perDocument}
}

func (l *referenceCatalogLimiter) AcquireReference(ctx context.Context, documentID string) (func(), error) {
	// A provider-style lease is intentionally reused here. The global key holds
	// all references; the document key prevents one bibliography monopolising it.
	releaseGlobal, err := l.throttle.Acquire(ctx, "reference-global", 0, l.global)
	if err != nil {
		return nil, err
	}
	releaseDocument, err := l.throttle.Acquire(ctx, "reference-document-"+documentID, 0, l.perDocument)
	if err != nil {
		releaseGlobal()
		return nil, err
	}
	return func() { releaseDocument(); releaseGlobal() }, nil
}

var _ interfaces.PaperReferenceCatalogLimiter = (*referenceCatalogLimiter)(nil)
