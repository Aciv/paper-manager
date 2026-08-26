package paperurl

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types/interfaces"

	"github.com/redis/go-redis/v9"
)

type downloadLimiter struct {
	spacing time.Duration
	max     int
	global  int
	memory  *memoryDownloadLimiter
	redis   *redis.Client
}

func NewDownloadLimiter(cfg config.Config) interfaces.RemoteDownloadLimiter {
	limiter := &downloadLimiter{
		spacing: cfg.RemotePDFHostSpacing,
		max:     cfg.RemotePDFHostConcurrency,
		global:  cfg.RemotePDFDownloadConcurrency,
		memory:  &memoryDownloadLimiter{state: make(map[string]downloadHostState)},
	}
	if addr := strings.TrimSpace(os.Getenv("REDIS_ADDR")); addr != "" {
		db, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("REDIS_DB")))
		limiter.redis = redis.NewClient(&redis.Options{Addr: addr,
			Username: strings.TrimSpace(os.Getenv("REDIS_USERNAME")), Password: os.Getenv("REDIS_PASSWORD"), DB: db})
	}
	return limiter
}

func (l *downloadLimiter) Acquire(ctx context.Context, host string) (func(), error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, fmt.Errorf("remote PDF host is required")
	}
	var releaseGlobal func()
	var err error
	if l.redis == nil {
		releaseGlobal, err = l.memory.acquire(ctx, "__global__", 0, l.global)
	} else {
		releaseGlobal, err = l.acquireRedis(ctx, "__global__", 0, l.global)
	}
	if err != nil {
		return nil, err
	}
	var releaseHost func()
	if l.redis == nil {
		releaseHost, err = l.memory.acquire(ctx, host, l.spacing, l.max)
	} else {
		releaseHost, err = l.acquireRedis(ctx, host, l.spacing, l.max)
	}
	if err != nil {
		releaseGlobal()
		return nil, err
	}
	return func() { releaseHost(); releaseGlobal() }, nil
}

func (l *downloadLimiter) Cooldown(ctx context.Context, host string, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if l.redis == nil {
		l.memory.cooldown(host, delay)
		return nil
	}
	now := time.Now()
	return l.redis.Eval(ctx, remoteCooldownScript, []string{downloadNextKey(host)}, now.Add(delay).UnixMilli(), now.UnixMilli()).Err()
}

type downloadHostState struct {
	next   time.Time
	active int
}
type memoryDownloadLimiter struct {
	mu    sync.Mutex
	state map[string]downloadHostState
}

func (l *memoryDownloadLimiter) acquire(ctx context.Context, host string, spacing time.Duration, max int) (func(), error) {
	if max < 1 {
		max = 1
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		l.mu.Lock()
		state := l.state[host]
		wait := time.Duration(0)
		if state.next.After(now) {
			wait = time.Until(state.next)
		} else if state.active >= max {
			wait = 25 * time.Millisecond
		} else {
			state.active++
			state.next = now.Add(spacing)
			l.state[host] = state
			l.mu.Unlock()
			return func() {
				l.mu.Lock()
				state := l.state[host]
				if state.active > 0 {
					state.active--
				}
				l.state[host] = state
				l.mu.Unlock()
			}, nil
		}
		l.mu.Unlock()
		if err := waitContext(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func (l *memoryDownloadLimiter) cooldown(host string, delay time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.state[host]
	if until := time.Now().Add(delay); state.next.Before(until) {
		state.next = until
	}
	l.state[host] = state
}

const remoteAcquireScript = `
local next_at = tonumber(redis.call('GET', KEYS[1]) or '0')
local now = tonumber(ARGV[1])
if next_at > now then return {0, next_at-now} end
local active = tonumber(redis.call('GET', KEYS[2]) or '0')
if active >= tonumber(ARGV[3]) then return {-1, 100} end
redis.call('SET', KEYS[1], now+tonumber(ARGV[2]), 'PX', math.max(tonumber(ARGV[2])*2,1000))
redis.call('INCR', KEYS[2]); redis.call('PEXPIRE', KEYS[2], tonumber(ARGV[4])); return {1,0}`

const remoteReleaseScript = `
local n=tonumber(redis.call('GET',KEYS[1]) or '0');
if n<=1 then redis.call('DEL',KEYS[1]) else redis.call('DECR',KEYS[1]) end;
return 1`

const remoteCooldownScript = `
local current=tonumber(redis.call('GET',KEYS[1]) or '0');
local proposed=tonumber(ARGV[1]);
if current<proposed then redis.call('SET',KEYS[1],proposed,'PX',math.max(proposed-tonumber(ARGV[2])+1000,1000)) end; 
return 1`

func downloadNextKey(host string) string   { return "papers:download:host:" + host + ":next" }
func downloadActiveKey(host string) string { return "papers:download:host:" + host + ":active" }

func (l *downloadLimiter) acquireRedis(ctx context.Context, host string, spacing time.Duration, max int) (func(), error) {
	if spacing <= 0 {
		spacing = time.Millisecond
	}
	if max < 1 {
		max = 1
	}
	for {
		result, err := l.redis.Eval(
			ctx, remoteAcquireScript,
			[]string{downloadNextKey(host), downloadActiveKey(host)},
			time.Now().UnixMilli(),
			spacing.Milliseconds(),
			max,
			(2 * time.Minute).Milliseconds()).Int64Slice()
		if err != nil {
			return nil, fmt.Errorf("remote PDF limiter unavailable: %w", err)
		}
		if len(result) == 2 && result[0] == 1 {
			return func() {
				_, _ = l.redis.Eval(context.Background(), remoteReleaseScript, []string{downloadActiveKey(host)}).Result()
			}, nil
		}
		wait := 100 * time.Millisecond
		if len(result) == 2 && result[1] > 0 {
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

var _ interfaces.RemoteDownloadLimiter = (*downloadLimiter)(nil)
