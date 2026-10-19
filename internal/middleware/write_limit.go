package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	apperrors "paperMaster/internal/errors"

	"github.com/gin-gonic/gin"
)

const (
	fallbackWriteRatePerMinute = 300
	fallbackWriteRateBurst     = 60
	fallbackUploadConcurrency  = 4
	fallbackBatchConcurrency   = 2
)

// WriteLimit bounds mutating HTTP requests per client IP and caps concurrent
// heavy upload/batch work. It runs after ErrorHandler so a 429 is serialized
// through the normal error envelope.
func WriteLimit(cfg config.Config) gin.HandlerFunc {
	perMinute := cfg.HTTPWriteRatePerMinute
	if perMinute <= 0 {
		perMinute = fallbackWriteRatePerMinute
	}
	burst := cfg.HTTPWriteRateBurst
	if burst <= 0 {
		burst = fallbackWriteRateBurst
	}
	uploadSlots := make(chan struct{}, positiveOr(cfg.HTTPUploadConcurrency, fallbackUploadConcurrency))
	batchSlots := make(chan struct{}, positiveOr(cfg.HTTPBatchConcurrency, fallbackBatchConcurrency))
	limiter := newIPRateLimiter(float64(perMinute)/60, burst)
	return func(c *gin.Context) {
		request := c.Request
		if request == nil || !isMutatingMethod(request.Method) {
			c.Next()
			return
		}
		if !limiter.Allow(c.ClientIP()) {
			c.Header("Retry-After", "1")
			c.Error(apperrors.NewTooManyRequestsError("too many write requests"))
			c.Abort()
			return
		}
		slots := heavyRequestSlots(request, uploadSlots, batchSlots)
		if slots != nil {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				c.Header("Retry-After", "1")
				c.Error(apperrors.NewTooManyRequestsError("too many concurrent heavy requests"))
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func heavyRequestSlots(request *http.Request, upload, batch chan struct{}) chan struct{} {
	path := request.URL.Path
	switch path {
	case "/api/document/upload", "/api/document/import-url":
		return upload
	case "/api/document/batch-upload", "/api/document/batch-import-url":
		return batch
	}
	if strings.HasSuffix(path, "/selection/process") {
		return upload
	}
	if strings.HasPrefix(path, "/api/references/") && strings.HasSuffix(path, "/download-pdf") {
		return batch
	}
	return nil
}

type rateBucket struct {
	tokens   float64
	lastSeen time.Time
}

// ipRateLimiter is a small in-process token bucket. Multi-instance deployments
// apply the limit per process; the outbound distributed limiter remains the
// authority for provider traffic.
type ipRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*rateBucket
}

func newIPRateLimiter(ratePerSecond float64, burst int) *ipRateLimiter {
	if ratePerSecond <= 0 {
		ratePerSecond = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &ipRateLimiter{rate: ratePerSecond, burst: float64(burst), buckets: make(map[string]*rateBucket)}
}

func (l *ipRateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	if strings.TrimSpace(key) == "" {
		key = "unknown"
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 4096 {
		for bucketKey, bucket := range l.buckets {
			if now.Sub(bucket.lastSeen) > time.Minute {
				delete(l.buckets, bucketKey)
			}
		}
	}
	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &rateBucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = bucket
	}
	elapsed := now.Sub(bucket.lastSeen).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * l.rate
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
		bucket.lastSeen = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
