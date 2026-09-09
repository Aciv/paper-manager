package papercatalog

import (
	"fmt"
	"net/http"
	"strings"

	"paperMaster/internal/config"
	"paperMaster/internal/limit"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

// Registry owns the configured provider set in deterministic order.
type Registry struct {
	byName map[string]interfaces.PaperCatalogProvider
	order  []interfaces.PaperCatalogProvider
}

// NewRegistry validates provider names and rejects duplicate registrations.
func NewRegistry(providers ...interfaces.PaperCatalogProvider) (*Registry, error) {
	registry := &Registry{
		byName: make(map[string]interfaces.PaperCatalogProvider, len(providers)),
		order:  make([]interfaces.PaperCatalogProvider, 0, len(providers)),
	}
	for _, provider := range providers {
		if provider == nil {
			return nil, fmt.Errorf("paper catalog provider cannot be nil")
		}
		name := strings.ToLower(strings.TrimSpace(provider.Name()))
		if name == "" {
			return nil, fmt.Errorf("paper catalog provider name is required")
		}
		if _, exists := registry.byName[name]; exists {
			return nil, fmt.Errorf("paper catalog provider %q is registered more than once", name)
		}
		registry.byName[name] = provider
		registry.order = append(registry.order, provider)
	}
	if len(registry.order) == 0 {
		return nil, fmt.Errorf("at least one paper catalog provider is required")
	}
	return registry, nil
}

// NewRegistryFromConfig creates only the providers selected by environment
// configuration. Each provider owns a timeout-bounded HTTP client so later
// provider-specific transport policy cannot leak into another adapter.
func NewRegistryFromConfig(cfg config.Config, shared *limit.Engine) (interfaces.PaperCatalogRegistry, error) {
	if cfg.PaperCatalogTimeout <= 0 {
		return nil, fmt.Errorf("paper catalog timeout must be positive")
	}
	newClient := func(provider string) *http.Client {
		return &http.Client{Timeout: cfg.PaperCatalogTimeoutFor(provider)}
	}
	throttle := newProviderThrottle(shared)
	providers := make([]interfaces.PaperCatalogProvider, 0, len(cfg.PaperCatalogProviders))
	for _, configuredName := range cfg.PaperCatalogProviders {
		name := types.NormalizePaperCatalogProvider(configuredName)
		var (
			provider interfaces.PaperCatalogProvider
			err      error
		)
		switch name {
		case types.PaperCatalogProviderSemanticScholar:
			provider, err = NewSemanticScholarProvider(
				newClient(name.String()),
				cfg.SemanticScholarAPIKey,
				cfg.PaperCatalogUserAgent,
			)
		case types.PaperCatalogProviderOpenAlex:
			provider, err = NewOpenAlexProvider(
				newClient(name.String()),
				cfg.OpenAlexAPIKey,
				cfg.PaperCatalogUserAgent,
			)
		case types.PaperCatalogProviderArXiv:
			provider, err = NewArXivProvider(
				newClient(name.String()), cfg.PaperCatalogUserAgent, 0,
			)
		case types.PaperCatalogProviderCrossref:
			provider, err = NewCrossrefProvider(
				newClient(name.String()),
				cfg.CrossrefMailto,
				cfg.PaperCatalogUserAgent,
			)
		default:
			return nil, fmt.Errorf("unsupported paper catalog provider %q", configuredName)
		}
		if err != nil {
			return nil, fmt.Errorf("initialize paper catalog provider %q: %w", name, err)
		}
		providers = append(providers, wrapProvider(provider, cfg, throttle))
	}
	return NewRegistry(providers...)
}

func (registry *Registry) Get(name string) (interfaces.PaperCatalogProvider, bool) {
	if registry == nil {
		return nil, false
	}
	provider, ok := registry.byName[strings.ToLower(strings.TrimSpace(name))]
	return provider, ok
}

func (registry *Registry) Providers() []interfaces.PaperCatalogProvider {
	if registry == nil {
		return nil
	}
	return append([]interfaces.PaperCatalogProvider(nil), registry.order...)
}

var _ interfaces.PaperCatalogRegistry = (*Registry)(nil)
