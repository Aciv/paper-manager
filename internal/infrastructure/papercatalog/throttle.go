package papercatalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/limit"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

const (
	catalogProviderNamespace = "catalog-provider"
)

// providerThrottle is a catalog-specific adapter over the shared limiter. It
// keeps provider naming and retry semantics out of the generic lease engine.
type providerThrottle struct {
	limiter limit.Limiter
}

func newProviderThrottle(limiter limit.Limiter) *providerThrottle {
	return &providerThrottle{limiter: limiter}
}

func (t *providerThrottle) Acquire(
	ctx context.Context, provider string, spacing time.Duration, max int, leaseTTL time.Duration,
) (func(), error) {
	if t == nil || t.limiter == nil {
		return nil, &throttledProviderError{provider: provider, retryAfter: time.Second, reason: "limiter is unavailable"}
	}
	release, err := t.limiter.Acquire(ctx, limit.Request{Dimensions: []limit.Dimension{{
		Namespace: catalogProviderNamespace, Key: provider,
		Spacing: spacing, Concurrency: max,
	}}, LeaseTTL: leaseTTL})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &throttledProviderError{provider: provider, retryAfter: time.Second, reason: "global limiter unavailable"}
	}
	return release, nil
}

func (t *providerThrottle) Cooldown(ctx context.Context, provider string, delay time.Duration) error {
	if t == nil || t.limiter == nil {
		return errors.New("catalog provider limiter is unavailable")
	}
	return t.limiter.Cooldown(ctx, limit.Dimension{
		Namespace: catalogProviderNamespace, Key: provider, Concurrency: 1,
	}, delay)
}

type rateLimitedProvider struct {
	provider    interfaces.PaperCatalogProvider
	throttle    *providerThrottle
	spacing     time.Duration
	concurrency int
	leaseTTL    time.Duration
}

func (p *rateLimitedProvider) Name() string { return p.provider.Name() }

func (p *rateLimitedProvider) Search(ctx context.Context, query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
	release, err := p.throttle.Acquire(ctx, p.Name(), p.spacing, p.concurrency, p.leaseTTL)
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

func wrapProvider(
	provider interfaces.PaperCatalogProvider, cfg config.Config, throttle *providerThrottle,
) interfaces.PaperCatalogProvider {
	return &rateLimitedProvider{provider: provider, throttle: throttle,
		spacing: cfg.PaperCatalogSpacingFor(provider.Name()), concurrency: cfg.PaperCatalogConcurrencyFor(provider.Name()),
		leaseTTL: cfg.PaperCatalogTimeoutFor(provider.Name()) + 30*time.Second}
}

// throttledProviderError implements queue retry contracts for shared limiter
// infrastructure failures. Ordinary contention waits inside Acquire.
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
