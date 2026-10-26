package papercatalog

import (
	"context"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/limit"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

type stubCatalogProvider string

func (provider stubCatalogProvider) Name() string {
	return string(provider)
}

func (stubCatalogProvider) Search(
	context.Context,
	types.PaperLookupQuery,
) ([]types.PaperCatalogCandidate, error) {
	return nil, nil
}

// TestRegistryPreservesOrderAndReturnsCopy verifies that registry enumeration
// keeps insertion order, returns a defensive copy, and supports normalized lookup.
func TestRegistryPreservesOrderAndReturnsCopy(t *testing.T) {
	registry, err := NewRegistry(
		stubCatalogProvider("first"),
		stubCatalogProvider("second"),
	)
	if err != nil {
		t.Fatal(err)
	}
	providers := registry.Providers()
	if len(providers) != 2 ||
		providers[0].Name() != "first" ||
		providers[1].Name() != "second" {
		t.Fatalf("unexpected order: %#v", providers)
	}
	providers[0] = nil
	if got := registry.Providers()[0]; got == nil {
		t.Fatal("caller mutated registry provider order")
	}
	if provider, ok := registry.Get(" FIRST "); !ok || provider.Name() != "first" {
		t.Fatalf("normalized lookup failed: %#v, %v", provider, ok)
	}
}

// TestRegistryRejectsDuplicateProvider verifies that provider names are
// normalized before duplicate detection, so case and spacing cannot bypass it.
func TestRegistryRejectsDuplicateProvider(t *testing.T) {
	_, err := NewRegistry(
		stubCatalogProvider("same"),
		stubCatalogProvider(" SAME "),
	)
	if err == nil {
		t.Fatal("expected duplicate provider error")
	}
}

// TestNewRegistryFromConfigSelectsProviders verifies that configured provider
// names are constructed in the declared order.
func TestNewRegistryFromConfigSelectsProviders(t *testing.T) {
	registry, err := NewRegistryFromConfig(config.Config{
		PaperCatalogProviders: []string{OpenAlexProvider, CrossrefProvider},
		PaperCatalogTimeout:   defaultCatalogTimeout,
		PaperCatalogUserAgent: "catalog-test/1.0",
	}, limit.NewMemoryEngine())
	if err != nil {
		t.Fatal(err)
	}
	providers := registry.Providers()
	if len(providers) != 2 ||
		providers[0].Name() != OpenAlexProvider ||
		providers[1].Name() != CrossrefProvider {
		t.Fatalf("unexpected providers: %#v", providers)
	}
}

// TestNewRegistryFromConfigRejectsUnknownProvider verifies that an unrecognized
// provider name fails registry construction instead of being ignored.
func TestNewRegistryFromConfigRejectsUnknownProvider(t *testing.T) {
	_, err := NewRegistryFromConfig(config.Config{
		PaperCatalogProviders: []string{"unknown"},
		PaperCatalogTimeout:   defaultCatalogTimeout,
	}, limit.NewMemoryEngine())
	if err == nil {
		t.Fatal("expected unknown provider error")
	}
}

// TestNewRegistryFromConfigRejectsUnboundedClient verifies that configuration
// without a positive catalog timeout is rejected to keep HTTP clients bounded.
func TestNewRegistryFromConfigRejectsUnboundedClient(t *testing.T) {
	_, err := NewRegistryFromConfig(config.Config{
		PaperCatalogProviders: []string{OpenAlexProvider},
	}, limit.NewMemoryEngine())
	if err == nil {
		t.Fatal("expected non-positive timeout error")
	}
}

var _ interfaces.PaperCatalogProvider = stubCatalogProvider("")
