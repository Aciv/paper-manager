package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from config_test.go.
// ---------------------------------------------------------------------------

// TestLoadPaperCatalogDefaults verifies that catalog provider, timeout,
// concurrency, and task retry settings fall back to the documented defaults
// when unset.
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

// TestLoadBibliographyDefaults verifies that bibliography limits, DOI lookup
// settings, caches, and task controls use their documented defaults when unset.
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

// TestLoadPaperCatalogConfiguration verifies that catalog environment variables
// are parsed, including provider list normalization, provider credentials, and
// task timing overrides.
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

// TestLoadRejectsInvalidPaperCatalogConcurrency verifies that a non-positive
// catalog concurrency is rejected at startup.
func TestLoadRejectsInvalidPaperCatalogConcurrency(t *testing.T) {
	t.Setenv("PAPER_CATALOG_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid catalog concurrency error")
	}
}

// TestLoadRejectsInvalidPaperCatalogTimeout verifies that a non-positive catalog
// timeout fails configuration loading.
func TestLoadRejectsInvalidPaperCatalogTimeout(t *testing.T) {
	t.Setenv("PAPER_CATALOG_TIMEOUT", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid catalog timeout error")
	}
}

// TestLoadRuntimeLimits verifies that HTTP, batch import, worker concurrency,
// and database pool limits are read from the environment as configured.
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

// TestLoadDocumentTaskConcurrencyDefaults verifies that document recognition
// and extraction concurrency both default to four when unset.
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

// TestLoadRejectsInvalidDocumentTaskConcurrency verifies that zero concurrency
// for either document recognition or extraction is rejected.
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

// TestLoadRejectsIdlePoolLargerThanOpenPool verifies that startup fails rather
// than silently configuring an unusable database pool.
func TestLoadRejectsIdlePoolLargerThanOpenPool(t *testing.T) {
	t.Setenv("DB_MAX_OPEN_CONNS", "10")
	t.Setenv("DB_MAX_IDLE_CONNS", "11")
	if _, err := Load(); err == nil {
		t.Fatal("expected idle pool validation error")
	}
}

// TestLoadRejectsInvalidJSONBodyLimit verifies that a non-positive JSON request
// body limit is rejected during configuration loading.
func TestLoadRejectsInvalidJSONBodyLimit(t *testing.T) {
	t.Setenv("HTTP_MAX_JSON_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid JSON body limit error")
	}
}

// TestLoadMCPMediaAndLocalImportDefaults verifies the default MCP image byte cap
// and that local file imports remain disabled without an explicit root.
func TestLoadMCPMediaAndLocalImportDefaults(t *testing.T) {
	t.Setenv("MCP_MAX_IMAGE_BYTES", "")
	t.Setenv("MCP_LOCAL_IMPORT_ROOT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPMaxImageBytes != 12<<20 {
		t.Fatalf("MCP max image bytes = %d, want %d", cfg.MCPMaxImageBytes, 12<<20)
	}
	if cfg.MCPLocalImportRoot != "" {
		t.Fatalf("MCP local import root = %q, want empty", cfg.MCPLocalImportRoot)
	}
}

// TestLoadMCPConfiguration verifies that the MCP image limit and local import
// root are honored from the environment and that zero image bytes is rejected.
func TestLoadMCPConfiguration(t *testing.T) {
	t.Setenv("MCP_MAX_IMAGE_BYTES", "2048")
	t.Setenv("MCP_LOCAL_IMPORT_ROOT", "/tmp/import-root")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPMaxImageBytes != 2048 || cfg.MCPLocalImportRoot != "/tmp/import-root" {
		t.Fatalf("MCP config = %#v", cfg)
	}

	t.Setenv("MCP_MAX_IMAGE_BYTES", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid MCP image limit error")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from dotenv_test.go.
// ---------------------------------------------------------------------------

// TestLoadDotEnvLoadsUnsetValuesAndPreservesProcessEnvironment verifies that
// dotenv loading fills unset variables and parses quoted values while leaving
// existing process values intact.
func TestLoadDotEnvLoadsUnsetValuesAndPreservesProcessEnvironment(t *testing.T) {
	const (
		loadedKey   = "PAPERS_MANAGER_DOTENV_TEST_LOADED"
		existingKey = "PAPERS_MANAGER_DOTENV_TEST_EXISTING"
		quotedKey   = "PAPERS_MANAGER_DOTENV_TEST_QUOTED"
	)
	unsetEnvForTest(t, loadedKey)
	unsetEnvForTest(t, quotedKey)
	t.Setenv(existingKey, "from-process")

	path := filepath.Join(t.TempDir(), ".env")
	content := "# local configuration\n" +
		loadedKey + "=from-file\n" +
		existingKey + "=must-not-replace\n" +
		quotedKey + "=\"quoted value\" # comment\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write dotenv fixture: %v", err)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv(loadedKey); got != "from-file" {
		t.Errorf("loaded value = %q, want %q", got, "from-file")
	}
	if got := os.Getenv(existingKey); got != "from-process" {
		t.Errorf("existing value = %q, want process value", got)
	}
	if got := os.Getenv(quotedKey); got != "quoted value" {
		t.Errorf("quoted value = %q, want %q", got, "quoted value")
	}
}

// TestLoadDotEnvRejectsMalformedLine verifies that a dotenv line without an
// assignment produces an error instead of being silently ignored.
func TestLoadDotEnvRejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("NOT_A_SETTING\n"), 0o600); err != nil {
		t.Fatalf("write dotenv fixture: %v", err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatal("LoadDotEnv() error = nil, want malformed-line error")
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func TestMCPConcurrencyConfiguration(t *testing.T) {
	for _, key := range []string{"MCP_READ_CONCURRENCY", "MCP_WRITE_CONCURRENCY", "MCP_HEAVY_CONCURRENCY", "MCP_IMPORT_CONCURRENCY"} {
		t.Setenv(key, "")
	}
	// The removed global option must no longer throttle or override any group.
	t.Setenv("MCP_TOOL_CONCURRENCY", "1")
	cfg, err := Load()
	if err != nil || cfg.MCPReadConcurrency != 128 || cfg.MCPWriteConcurrency != 32 ||
		cfg.MCPHeavyConcurrency != 4 || cfg.MCPImportConcurrency != 2 {
		t.Fatalf("unexpected defaults: read=%d write=%d heavy=%d import=%d err=%v",
			cfg.MCPReadConcurrency, cfg.MCPWriteConcurrency, cfg.MCPHeavyConcurrency, cfg.MCPImportConcurrency, err)
	}
	t.Setenv("MCP_READ_CONCURRENCY", "256")
	t.Setenv("MCP_WRITE_CONCURRENCY", "64")
	t.Setenv("MCP_HEAVY_CONCURRENCY", "8")
	t.Setenv("MCP_IMPORT_CONCURRENCY", "1")
	cfg, err = Load()
	if err != nil || cfg.MCPReadConcurrency != 256 || cfg.MCPWriteConcurrency != 64 ||
		cfg.MCPHeavyConcurrency != 8 || cfg.MCPImportConcurrency != 1 {
		t.Fatalf("group overrides not applied: %v", err)
	}
	for _, test := range []struct{ key, tooLarge string }{
		{"MCP_READ_CONCURRENCY", "1001"}, {"MCP_WRITE_CONCURRENCY", "129"},
		{"MCP_HEAVY_CONCURRENCY", "33"}, {"MCP_IMPORT_CONCURRENCY", "1001"},
	} {
		t.Run(test.key, func(t *testing.T) {
			for _, value := range []string{"0", "-1", test.tooLarge, "invalid"} {
				t.Setenv(test.key, value)
				if _, err := Load(); err == nil {
					t.Fatalf("invalid %s accepted: %s", test.key, value)
				}
			}
		})
	}
}
