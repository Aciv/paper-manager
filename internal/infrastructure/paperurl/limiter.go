package paperurl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/limit"
	"paperMaster/internal/types/interfaces"
)

const remoteDownloadNamespace = "remote-download"

// downloadLimiter adapts host/global PDF download policy to the shared limiter.
// Both dimensions are acquired atomically, so a task waiting for one busy host
// never consumes a global download slot.
type downloadLimiter struct {
	limiter  limit.Limiter
	spacing  time.Duration
	max      int
	global   int
	leaseTTL time.Duration
}

func NewDownloadLimiter(cfg config.Config, shared *limit.Engine) interfaces.RemoteDownloadLimiter {
	global, perHost := cfg.RemotePDFDownloadConcurrency, cfg.RemotePDFHostConcurrency
	if global <= 0 {
		global = 1
	}
	if perHost <= 0 {
		perHost = 1
	}
	return &downloadLimiter{
		limiter:  shared,
		spacing:  cfg.RemotePDFHostSpacing,
		max:      perHost,
		global:   global,
		leaseTTL: cfg.RemotePDFDownloadTimeout + 30*time.Second,
	}
}

func (l *downloadLimiter) Acquire(ctx context.Context, host string) (func(), error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, fmt.Errorf("remote PDF host is required")
	}
	if l == nil || l.limiter == nil {
		return nil, errors.New("remote PDF limiter is unavailable")
	}
	return l.limiter.Acquire(ctx, limit.Request{Dimensions: []limit.Dimension{
		{Namespace: remoteDownloadNamespace, Key: "global", Concurrency: l.global},
		{Namespace: remoteDownloadNamespace, Key: host, Concurrency: l.max, Spacing: l.spacing},
	}, LeaseTTL: l.leaseTTL})
}

func (l *downloadLimiter) Cooldown(ctx context.Context, host string, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return errors.New("remote PDF host is required")
	}
	if l == nil || l.limiter == nil {
		return errors.New("remote PDF limiter is unavailable")
	}
	return l.limiter.Cooldown(ctx, limit.Dimension{
		Namespace: remoteDownloadNamespace, Key: host, Concurrency: l.max,
	}, delay)
}

var _ interfaces.RemoteDownloadLimiter = (*downloadLimiter)(nil)
