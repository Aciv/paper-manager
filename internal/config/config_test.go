package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadPaperCatalogDefaults(t *testing.T) {
	for _, key := range []string{
		"PAPER_CATALOG_PROVIDERS",
		"PAPER_CATALOG_TIMEOUT",
		"PAPER_CATALOG_CONCURRENCY",
		"PAPER_CATALOG_USER_AGENT",
		"SEMANTIC_SCHOLAR_API_KEY",
		"OPENALEX_API_KEY",
		"CROSSREF_MAILTO",
	} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wantProviders := []string{"arxiv", "semantic_scholar", "openalex", "crossref"}
	if !reflect.DeepEqual(cfg.PaperCatalogProviders, wantProviders) {
		t.Fatalf("providers = %#v, want %#v", cfg.PaperCatalogProviders, wantProviders)
	}
	if cfg.PaperCatalogTimeout != 10*time.Second {
		t.Fatalf("timeout = %s", cfg.PaperCatalogTimeout)
	}
	if cfg.PaperCatalogConcurrency != 4 {
		t.Fatalf("concurrency = %d, want 4", cfg.PaperCatalogConcurrency)
	}
}

func TestLoadPaperCatalogConfiguration(t *testing.T) {
	t.Setenv("PAPER_CATALOG_PROVIDERS", " OpenAlex, crossref,openalex ")
	t.Setenv("PAPER_CATALOG_TIMEOUT", "2500ms")
	t.Setenv("PAPER_CATALOG_CONCURRENCY", "2")
	t.Setenv("PAPER_CATALOG_USER_AGENT", "papers-test/2.0")
	t.Setenv("SEMANTIC_SCHOLAR_API_KEY", "semantic-key")
	t.Setenv("OPENALEX_API_KEY", "openalex-key")
	t.Setenv("CROSSREF_MAILTO", "ops@example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wantProviders := []string{"openalex", "crossref"}
	if !reflect.DeepEqual(cfg.PaperCatalogProviders, wantProviders) {
		t.Fatalf("providers = %#v, want %#v", cfg.PaperCatalogProviders, wantProviders)
	}
	if cfg.PaperCatalogTimeout != 2500*time.Millisecond ||
		cfg.PaperCatalogUserAgent != "papers-test/2.0" ||
		cfg.PaperCatalogConcurrency != 2 ||
		cfg.OpenAlexAPIKey != "openalex-key" ||
		cfg.CrossrefMailto != "ops@example.test" {
		t.Fatalf("unexpected catalog config: %#v", cfg)
	}
}

func TestLoadRejectsInvalidPaperCatalogConcurrency(t *testing.T) {
	t.Setenv("PAPER_CATALOG_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid catalog concurrency error")
	}
}

func TestLoadRejectsInvalidPaperCatalogTimeout(t *testing.T) {
	t.Setenv("PAPER_CATALOG_TIMEOUT", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid catalog timeout error")
	}
}
