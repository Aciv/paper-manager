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
		"PAPER_EXTRACTION_TASK_TIMEOUT",
		"PAPER_EXTRACTION_TASK_MAX_RETRY",
		"PAPER_CATALOG_TASK_TIMEOUT",
		"PAPER_CATALOG_TASK_MAX_RETRY",
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
	if cfg.PaperExtractionTaskTimeout != 2*time.Minute ||
		cfg.PaperExtractionTaskMaxRetry != 3 ||
		cfg.PaperCatalogTaskTimeout != 10*time.Minute ||
		cfg.PaperCatalogTaskMaxRetry != 5 {
		t.Fatalf("unexpected paper task defaults: %#v", cfg)
	}
}

func TestLoadBibliographyDefaults(t *testing.T) {
	for _, key := range []string{
		"BIBLIOGRAPHY_MAX_ENTRIES", "BIBLIOGRAPHY_DOI_CONCURRENCY", "BIBLIOGRAPHY_DOI_TIMEOUT",
		"BIBLIOGRAPHY_TOTAL_TIMEOUT", "BIBLIOGRAPHY_DOI_MAX_BODY_BYTES", "BIBLIOGRAPHY_MAX_OUTPUT_BYTES",
		"BIBLIOGRAPHY_MAX_RESPONSE_BYTES",
		"BIBLIOGRAPHY_TASK_CONCURRENCY", "BIBLIOGRAPHY_TASK_TIMEOUT",
		"BIBLIOGRAPHY_TASK_MAX_RETRY", "BIBLIOGRAPHY_RESULT_RETENTION",
		"BIBLIOGRAPHY_DOI_CACHE_ENTRIES", "BIBLIOGRAPHY_DOI_CACHE_TTL",
	} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BibliographyMaxEntries != 200 || cfg.BibliographyDOIConcurrency != 4 ||
		cfg.BibliographyDOITimeout != 5*time.Second || cfg.BibliographyTotalTimeout != 30*time.Second ||
		cfg.BibliographyDOIMaxBodyBytes != 256<<10 || cfg.BibliographyMaxOutputBytes != 8<<20 ||
		cfg.BibliographyMaxResponseBytes != 12<<20 ||
		cfg.BibliographyDOICacheEntries != 100 || cfg.BibliographyDOICacheTTL != 30*time.Minute ||
		cfg.BibliographyTaskConcurrency != 2 || cfg.BibliographyTaskTimeout != 45*time.Second ||
		cfg.BibliographyTaskMaxRetry != 2 || cfg.BibliographyResultRetention != 30*time.Minute {
		t.Fatalf("unexpected bibliography defaults: %#v", cfg)
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
	t.Setenv("PAPER_EXTRACTION_TASK_TIMEOUT", "45s")
	t.Setenv("PAPER_EXTRACTION_TASK_MAX_RETRY", "4")
	t.Setenv("PAPER_CATALOG_TASK_TIMEOUT", "3m")
	t.Setenv("PAPER_CATALOG_TASK_MAX_RETRY", "7")

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
		cfg.CrossrefMailto != "ops@example.test" ||
		cfg.PaperExtractionTaskTimeout != 45*time.Second ||
		cfg.PaperExtractionTaskMaxRetry != 4 ||
		cfg.PaperCatalogTaskTimeout != 3*time.Minute ||
		cfg.PaperCatalogTaskMaxRetry != 7 {
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

func TestLoadRuntimeLimits(t *testing.T) {
	t.Setenv("HTTP_READ_TIMEOUT", "3m")
	t.Setenv("HTTP_WRITE_TIMEOUT", "7m")
	t.Setenv("HTTP_IDLE_TIMEOUT", "90s")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "45s")
	t.Setenv("HTTP_MAX_JSON_BYTES", "2048")
	t.Setenv("HTTP_WRITE_RATE_PER_MINUTE", "120")
	t.Setenv("HTTP_WRITE_RATE_BURST", "20")
	t.Setenv("HTTP_UPLOAD_CONCURRENCY", "3")
	t.Setenv("HTTP_BATCH_CONCURRENCY", "2")
	t.Setenv("HTTP_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.0.0/16")
	t.Setenv("BATCH_IMPORT_TIMEOUT", "45s")
	t.Setenv("ASYNQ_CONCURRENCY", "12")
	t.Setenv("DOCUMENT_RECOGNITION_CONCURRENCY", "4")
	t.Setenv("DOCUMENT_EXTRACTION_CONCURRENCY", "3")
	t.Setenv("ASYNQ_SHUTDOWN_TIMEOUT", "20s")
	t.Setenv("DB_MAX_OPEN_CONNS", "30")
	t.Setenv("DB_MAX_IDLE_CONNS", "15")
	t.Setenv("DB_STATEMENT_TIMEOUT", "8s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPReadTimeout != 3*time.Minute ||
		cfg.HTTPWriteTimeout != 7*time.Minute ||
		cfg.HTTPIdleTimeout != 90*time.Second ||
		cfg.HTTPShutdownTimeout != 45*time.Second ||
		cfg.HTTPMaxJSONBytes != 2048 ||
		cfg.HTTPWriteRatePerMinute != 120 ||
		cfg.HTTPWriteRateBurst != 20 ||
		cfg.HTTPUploadConcurrency != 3 ||
		cfg.HTTPBatchConcurrency != 2 ||
		len(cfg.HTTPTrustedProxies) != 2 ||
		cfg.BatchImportTimeout != 45*time.Second ||
		cfg.AsynqConcurrency != 12 ||
		cfg.DocumentRecognitionConcurrency != 4 ||
		cfg.DocumentExtractionConcurrency != 3 ||
		cfg.AsynqShutdownTimeout != 20*time.Second ||
		cfg.DBMaxOpenConns != 30 ||
		cfg.DBMaxIdleConns != 15 ||
		cfg.DBStatementTimeout != 8*time.Second {
		t.Fatalf("unexpected runtime limits: %#v", cfg)
	}
}

func TestLoadDocumentTaskConcurrencyDefaults(t *testing.T) {
	t.Setenv("DOCUMENT_RECOGNITION_CONCURRENCY", "")
	t.Setenv("DOCUMENT_EXTRACTION_CONCURRENCY", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DocumentRecognitionConcurrency != 4 || cfg.DocumentExtractionConcurrency != 4 {
		t.Fatalf(
			"document task concurrency = recognition %d, extraction %d; want 4 and 4",
			cfg.DocumentRecognitionConcurrency,
			cfg.DocumentExtractionConcurrency,
		)
	}
}

func TestLoadRejectsInvalidDocumentTaskConcurrency(t *testing.T) {
	t.Setenv("DOCUMENT_RECOGNITION_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid document recognition concurrency error")
	}

	t.Setenv("DOCUMENT_RECOGNITION_CONCURRENCY", "4")
	t.Setenv("DOCUMENT_EXTRACTION_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid document extraction concurrency error")
	}
}

func TestLoadRejectsIdlePoolLargerThanOpenPool(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "10")
	t.Setenv("DB_MAX_IDLE_CONNS", "11")
	if _, err := Load(); err == nil {
		t.Fatal("expected idle pool validation error")
	}
}

func TestLoadRejectsInvalidJSONBodyLimit(t *testing.T) {
	t.Setenv("HTTP_MAX_JSON_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid JSON body limit error")
	}
}
