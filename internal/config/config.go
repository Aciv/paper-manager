// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultMaxUploadSize int64 = 100 << 20 // 100 MiB

const (
	defaultDocumentHousekeepingInterval   = 5 * time.Minute
	defaultDocumentStaleThreshold         = time.Hour + 10*time.Minute
	defaultProcessingSpanSuccessRetention = 30 * 24 * time.Hour
	defaultProcessingSpanFailureRetention = 90 * 24 * time.Hour
	defaultProcessingSpanRetentionBatch   = 500
	defaultPaperCatalogTimeout            = 10 * time.Second
	defaultPaperCatalogConcurrency        = 4
	defaultPaperCatalogSourceStopScore    = 0.9
	defaultPaperExtractionTaskTimeout     = 2 * time.Minute
	defaultPaperExtractionTaskMaxRetry    = 3
	defaultPaperCatalogTaskTimeout        = 10 * time.Minute
	defaultPaperCatalogTaskMaxRetry       = 5
	// defaultPaperReferenceCatalogMaxPerDocument is the per-document
	// admission budget, not a running-concurrency bound: it caps how many
	// unresolved citations one dispatch submits to the in-process dispatcher,
	// while actual parallelism is governed by the reference admission caps and
	// provider spacing. It must comfortably exceed realistic bibliography
	// sizes so a paper's whole reference list is submitted in one round; there
	// is no automatic continuation beyond this cap.
	defaultPaperReferenceCatalogMaxPerDocument               = 200
	defaultPaperReferenceCatalogConcurrency                  = 2
	defaultPaperReferenceCatalogPerDocumentConcurrency       = 1
	defaultRemotePDFDownloadTimeout                          = 2 * time.Minute
	defaultRemotePDFMaxRedirects                             = 5
	defaultRemotePDFDownloadConcurrency                      = 2
	defaultRemotePDFHostConcurrency                          = 1
	defaultRemotePDFHostSpacing                              = time.Second
	defaultHTTPReadTimeout                                   = 5 * time.Minute
	defaultHTTPWriteTimeout                                  = 10 * time.Minute
	defaultHTTPIdleTimeout                                   = 2 * time.Minute
	defaultHTTPShutdownTimeout                               = 30 * time.Second
	defaultHTTPMaxJSONBytes                            int64 = 1 << 20 // 1 MiB
	defaultHTTPWriteRatePerMinute                            = 300
	defaultHTTPWriteRateBurst                                = 60
	defaultHTTPUploadConcurrency                             = 4
	defaultHTTPBatchConcurrency                              = 2
	defaultBatchImportTimeout                                = 2 * time.Minute
	defaultAsynqConcurrency                                  = 32
	defaultDocumentRecognitionConcurrency                    = 4
	defaultDocumentExtractionConcurrency                     = 4
	defaultAsynqShutdownTimeout                              = 30 * time.Second
	defaultDBMaxOpenConns                                    = 48
	defaultDBMaxIdleConns                                    = 24
	defaultDBStatementTimeout                                = 30 * time.Second
)

// Config contains the infrastructure options selected at process startup.
type Config struct {
	HTTPAddress string
	// HTTP timeouts bound slow clients. Uploads and content downloads need a
	// generous read/write budget; idle keep-alive connections do not.
	HTTPReadTimeout     time.Duration
	HTTPWriteTimeout    time.Duration
	HTTPIdleTimeout     time.Duration
	HTTPShutdownTimeout time.Duration
	// HTTPMaxJSONBytes bounds non-multipart JSON request bodies. Multipart
	// uploads keep their existing per-file and per-batch limits.
	HTTPMaxJSONBytes int64
	// HTTPWriteRate* bound mutating requests per client IP. Concurrency limits
	// separately cap in-flight uploads and batch imports.
	HTTPWriteRatePerMinute int
	HTTPWriteRateBurst     int
	HTTPUploadConcurrency  int
	HTTPBatchConcurrency   int
	// HTTPTrustedProxies lists proxy CIDRs whose X-Forwarded-For is trusted.
	// Empty means ClientIP uses RemoteAddr (no proxy spoofing).
	HTTPTrustedProxies []string
	// BatchImportTimeout caps one synchronous batch URL import request; each
	// item also keeps its own remote download limits.
	BatchImportTimeout time.Duration
	DatabaseDriver     string
	DatabaseURL        string
	// DB pool sizing must cover Asynq workers plus reserved HTTP/background
	// connections; statement timeout bounds one query's hold on a connection.
	DBMaxOpenConns     int
	DBMaxIdleConns     int
	DBStatementTimeout time.Duration
	CORSOrigin         string
	FileStorageDriver  string
	UploadDirectory    string
	// RecognitionOutputDirectory is the shared, trusted root where a
	// directory-mode recognizer may create an output package. The API copies
	// only the Markdown file from this directory into durable FileStorage.
	RecognitionOutputDirectory     string
	MaxUploadSize                  int64
	AutoMigrate                    bool
	DocumentHousekeepingEnabled    bool
	DocumentHousekeepingInterval   time.Duration
	DocumentStaleThreshold         time.Duration
	ProcessingSpanRetentionEnabled bool
	ProcessingSpanSuccessRetention time.Duration
	ProcessingSpanFailureRetention time.Duration
	ProcessingSpanRetentionBatch   int
	DocumentRecognitionDriver      string
	AsynqConcurrency               int
	DocumentRecognitionConcurrency int
	DocumentExtractionConcurrency  int
	AsynqShutdownTimeout           time.Duration
	ParserGRPCAddress              string
	ParserGRPCMaxMessageSize       int
	PaperCatalogProviders          []string
	PaperCatalogTimeout            time.Duration
	// PaperCatalogConcurrency is retained for configuration compatibility.
	// Source provider fan-out moved to the in-process dispatcher, which runs
	// one worker per configured provider.
	PaperCatalogConcurrency int
	// Per-provider policies override the legacy shared timeout/concurrency.
	// Keys use the stable catalog provider name (for example "arxiv").
	PaperCatalogProviderTimeouts                map[string]time.Duration
	PaperCatalogProviderSpacings                map[string]time.Duration
	PaperCatalogProviderConcurrency             map[string]int
	PaperCatalogUserAgent                       string
	SemanticScholarAPIKey                       string
	OpenAlexAPIKey                              string
	CrossrefMailto                              string
	PaperExtractionTaskTimeout                  time.Duration
	PaperExtractionTaskMaxRetry                 int
	PaperCatalogTaskTimeout                     time.Duration
	PaperCatalogTaskMaxRetry                    int
	PaperCatalogSourceStopScore                 float64
	PaperReferenceCatalogMaxPerDocument         int
	PaperReferenceCatalogConcurrency            int
	PaperReferenceCatalogPerDocumentConcurrency int
	// PaperReferenceAutoResolve enables external provider lookups immediately
	// after extraction. Local DOI/title matching always runs regardless.
	PaperReferenceAutoResolve    bool
	RemotePDFDownloadTimeout     time.Duration
	RemotePDFMaxRedirects        int
	RemotePDFDownloadConcurrency int
	RemotePDFHostConcurrency     int
	RemotePDFHostSpacing         time.Duration
}

func Load() (Config, error) {
	uploadDirectory := env("UPLOAD_DIR", "./data/uploads")
	maxUploadSize := DefaultMaxUploadSize
	if value := strings.TrimSpace(os.Getenv("FILE_MAX_UPLOAD_SIZE")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("FILE_MAX_UPLOAD_SIZE must be a positive byte count")
		}
		maxUploadSize = parsed
	}
	housekeepingInterval, err := durationEnv("DOCUMENT_HOUSEKEEPING_INTERVAL", defaultDocumentHousekeepingInterval)
	if err != nil {
		return Config{}, err
	}
	staleThreshold, err := durationEnv("DOCUMENT_STALE_THRESHOLD", defaultDocumentStaleThreshold)
	if err != nil {
		return Config{}, err
	}
	if staleThreshold < housekeepingInterval {
		return Config{}, fmt.Errorf("DOCUMENT_STALE_THRESHOLD must be at least DOCUMENT_HOUSEKEEPING_INTERVAL")
	}
	spanSuccessRetention, err := durationEnv("PROCESSING_SPAN_SUCCESS_RETENTION", defaultProcessingSpanSuccessRetention)
	if err != nil || spanSuccessRetention <= 0 {
		return Config{}, fmt.Errorf("PROCESSING_SPAN_SUCCESS_RETENTION must be a positive duration")
	}
	spanFailureRetention, err := durationEnv("PROCESSING_SPAN_FAILURE_RETENTION", defaultProcessingSpanFailureRetention)
	if err != nil || spanFailureRetention < spanSuccessRetention {
		return Config{}, fmt.Errorf("PROCESSING_SPAN_FAILURE_RETENTION must be at least PROCESSING_SPAN_SUCCESS_RETENTION")
	}
	spanRetentionBatch, err := positiveIntEnv("PROCESSING_SPAN_RETENTION_BATCH", defaultProcessingSpanRetentionBatch, 5000)
	if err != nil {
		return Config{}, err
	}
	parserMessageSize := maxUploadSize + (1 << 20)
	if value := strings.TrimSpace(os.Getenv("PARSER_GRPC_MAX_MESSAGE_SIZE")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 || parsed > int64(^uint(0)>>1) {
			return Config{}, fmt.Errorf("PARSER_GRPC_MAX_MESSAGE_SIZE must be a positive supported byte count")
		}
		parserMessageSize = parsed
	}
	paperCatalogTimeout, err := durationEnv("PAPER_CATALOG_TIMEOUT", defaultPaperCatalogTimeout)
	if err != nil {
		return Config{}, err
	}
	paperCatalogConcurrency, err := positiveIntEnv(
		"PAPER_CATALOG_CONCURRENCY",
		defaultPaperCatalogConcurrency,
		32,
	)
	if err != nil {
		return Config{}, err
	}
	paperCatalogSourceStopScore := defaultPaperCatalogSourceStopScore
	if value := strings.TrimSpace(os.Getenv("PAPER_CATALOG_SOURCE_STOP_SCORE")); value != "" {
		parsed, parseErr := strconv.ParseFloat(value, 64)
		if parseErr != nil || parsed <= 0 || parsed > 1 {
			return Config{}, fmt.Errorf("PAPER_CATALOG_SOURCE_STOP_SCORE must be between 0 and 1")
		}
		paperCatalogSourceStopScore = parsed
	}
	paperCatalogProviders := listEnv(
		"PAPER_CATALOG_PROVIDERS",
		"arxiv,semantic_scholar,openalex,crossref",
	)
	if len(paperCatalogProviders) == 0 {
		return Config{}, fmt.Errorf("PAPER_CATALOG_PROVIDERS must contain at least one provider")
	}
	providerTimeouts, providerSpacings, providerConcurrency, err := paperCatalogProviderPolicy(paperCatalogProviders, paperCatalogTimeout)
	if err != nil {
		return Config{}, err
	}
	paperExtractionTaskTimeout, err := durationEnv(
		"PAPER_EXTRACTION_TASK_TIMEOUT",
		defaultPaperExtractionTaskTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	paperExtractionTaskMaxRetry, err := positiveIntEnv(
		"PAPER_EXTRACTION_TASK_MAX_RETRY",
		defaultPaperExtractionTaskMaxRetry,
		20,
	)
	if err != nil {
		return Config{}, err
	}
	paperCatalogTaskTimeout, err := durationEnv(
		"PAPER_CATALOG_TASK_TIMEOUT",
		defaultPaperCatalogTaskTimeout,
	)
	if err != nil {
		return Config{}, err
	}
	paperCatalogTaskMaxRetry, err := positiveIntEnv(
		"PAPER_CATALOG_TASK_MAX_RETRY",
		defaultPaperCatalogTaskMaxRetry,
		20,
	)
	if err != nil {
		return Config{}, err
	}
	paperReferenceCatalogMaxPerDocument, err := positiveIntEnv(
		"PAPER_REFERENCE_CATALOG_MAX_PER_DOCUMENT",
		defaultPaperReferenceCatalogMaxPerDocument,
		500,
	)
	if err != nil {
		return Config{}, err
	}
	paperReferenceCatalogConcurrency, err := positiveIntEnv("PAPER_REFERENCE_CATALOG_CONCURRENCY", defaultPaperReferenceCatalogConcurrency, 32)
	if err != nil {
		return Config{}, err
	}
	paperReferenceCatalogPerDocumentConcurrency, err := positiveIntEnv("PAPER_REFERENCE_CATALOG_PER_DOCUMENT_CONCURRENCY", defaultPaperReferenceCatalogPerDocumentConcurrency, 16)
	if err != nil {
		return Config{}, err
	}
	paperReferenceAutoResolve := false
	if value := strings.TrimSpace(os.Getenv("PAPER_REFERENCE_AUTO_RESOLVE")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("PAPER_REFERENCE_AUTO_RESOLVE must be true or false")
		}
		paperReferenceAutoResolve = parsed
	}
	remotePDFDownloadTimeout, err := durationEnv("REMOTE_PDF_DOWNLOAD_TIMEOUT", defaultRemotePDFDownloadTimeout)
	if err != nil {
		return Config{}, err
	}
	remotePDFMaxRedirects, err := positiveIntEnv("REMOTE_PDF_MAX_REDIRECTS", defaultRemotePDFMaxRedirects, 20)
	if err != nil {
		return Config{}, err
	}
	remotePDFDownloadConcurrency, err := positiveIntEnv("REMOTE_PDF_DOWNLOAD_CONCURRENCY", defaultRemotePDFDownloadConcurrency, 32)
	if err != nil {
		return Config{}, err
	}
	remotePDFHostConcurrency, err := positiveIntEnv("REMOTE_PDF_HOST_CONCURRENCY", defaultRemotePDFHostConcurrency, 16)
	if err != nil {
		return Config{}, err
	}
	remotePDFHostSpacing, err := durationEnv("REMOTE_PDF_HOST_SPACING", defaultRemotePDFHostSpacing)
	if err != nil {
		return Config{}, err
	}
	httpReadTimeout, err := durationEnv("HTTP_READ_TIMEOUT", defaultHTTPReadTimeout)
	if err != nil {
		return Config{}, err
	}
	httpWriteTimeout, err := durationEnv("HTTP_WRITE_TIMEOUT", defaultHTTPWriteTimeout)
	if err != nil {
		return Config{}, err
	}
	httpIdleTimeout, err := durationEnv("HTTP_IDLE_TIMEOUT", defaultHTTPIdleTimeout)
	if err != nil {
		return Config{}, err
	}
	httpShutdownTimeout, err := durationEnv("HTTP_SHUTDOWN_TIMEOUT", defaultHTTPShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	httpMaxJSONBytes, err := positiveInt64Env("HTTP_MAX_JSON_BYTES", defaultHTTPMaxJSONBytes, 64<<20)
	if err != nil {
		return Config{}, err
	}
	httpWriteRatePerMinute, err := positiveIntEnv("HTTP_WRITE_RATE_PER_MINUTE", defaultHTTPWriteRatePerMinute, 100000)
	if err != nil {
		return Config{}, err
	}
	httpWriteRateBurst, err := positiveIntEnv("HTTP_WRITE_RATE_BURST", defaultHTTPWriteRateBurst, 100000)
	if err != nil {
		return Config{}, err
	}
	httpUploadConcurrency, err := positiveIntEnv("HTTP_UPLOAD_CONCURRENCY", defaultHTTPUploadConcurrency, 1000)
	if err != nil {
		return Config{}, err
	}
	httpBatchConcurrency, err := positiveIntEnv("HTTP_BATCH_CONCURRENCY", defaultHTTPBatchConcurrency, 1000)
	if err != nil {
		return Config{}, err
	}
	batchImportTimeout, err := durationEnv("BATCH_IMPORT_TIMEOUT", defaultBatchImportTimeout)
	if err != nil {
		return Config{}, err
	}
	var httpTrustedProxies []string
	for _, proxy := range strings.Split(env("HTTP_TRUSTED_PROXIES", ""), ",") {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			httpTrustedProxies = append(httpTrustedProxies, proxy)
		}
	}
	asynqConcurrency, err := positiveIntEnv("ASYNQ_CONCURRENCY", defaultAsynqConcurrency, 512)
	if err != nil {
		return Config{}, err
	}
	documentRecognitionConcurrency, err := positiveIntEnv(
		"DOCUMENT_RECOGNITION_CONCURRENCY",
		defaultDocumentRecognitionConcurrency,
		512,
	)
	if err != nil {
		return Config{}, err
	}
	documentExtractionConcurrency, err := positiveIntEnv(
		"DOCUMENT_EXTRACTION_CONCURRENCY",
		defaultDocumentExtractionConcurrency,
		512,
	)
	if err != nil {
		return Config{}, err
	}
	asynqShutdownTimeout, err := durationEnv("ASYNQ_SHUTDOWN_TIMEOUT", defaultAsynqShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	dbMaxOpenConns, err := positiveIntEnv("DB_MAX_OPEN_CONNS", defaultDBMaxOpenConns, 500)
	if err != nil {
		return Config{}, err
	}
	dbMaxIdleConns, err := positiveIntEnv("DB_MAX_IDLE_CONNS", defaultDBMaxIdleConns, 500)
	if err != nil {
		return Config{}, err
	}
	if dbMaxIdleConns > dbMaxOpenConns {
		return Config{}, fmt.Errorf("DB_MAX_IDLE_CONNS must not exceed DB_MAX_OPEN_CONNS")
	}
	dbStatementTimeout, err := durationEnv("DB_STATEMENT_TIMEOUT", defaultDBStatementTimeout)
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddress:                                 env("HTTP_ADDR", "127.0.0.1:8080"),
		HTTPReadTimeout:                             httpReadTimeout,
		HTTPWriteTimeout:                            httpWriteTimeout,
		HTTPIdleTimeout:                             httpIdleTimeout,
		HTTPShutdownTimeout:                         httpShutdownTimeout,
		HTTPMaxJSONBytes:                            httpMaxJSONBytes,
		HTTPWriteRatePerMinute:                      httpWriteRatePerMinute,
		HTTPWriteRateBurst:                          httpWriteRateBurst,
		HTTPUploadConcurrency:                       httpUploadConcurrency,
		HTTPBatchConcurrency:                        httpBatchConcurrency,
		HTTPTrustedProxies:                          httpTrustedProxies,
		BatchImportTimeout:                          batchImportTimeout,
		DatabaseDriver:                              strings.ToLower(env("DB_DRIVER", "postgres")),
		DatabaseURL:                                 env("DATABASE_URL", "postgres://papers:papers@localhost:5432/papers?sslmode=disable"),
		DBMaxOpenConns:                              dbMaxOpenConns,
		DBMaxIdleConns:                              dbMaxIdleConns,
		DBStatementTimeout:                          dbStatementTimeout,
		CORSOrigin:                                  env("CORS_ORIGIN", "http://localhost:5173"),
		FileStorageDriver:                           strings.ToLower(env("FILE_STORAGE_DRIVER", "local")),
		UploadDirectory:                             uploadDirectory,
		RecognitionOutputDirectory:                  env("RECOGNITION_OUTPUT_DIR", filepath.Join(uploadDirectory, "recognition-output")),
		MaxUploadSize:                               maxUploadSize,
		AutoMigrate:                                 !strings.EqualFold(env("AUTO_MIGRATE", "true"), "false"),
		DocumentHousekeepingEnabled:                 !strings.EqualFold(env("DOCUMENT_HOUSEKEEPING_ENABLED", "true"), "false"),
		DocumentHousekeepingInterval:                housekeepingInterval,
		DocumentStaleThreshold:                      staleThreshold,
		ProcessingSpanRetentionEnabled:              !strings.EqualFold(env("PROCESSING_SPAN_RETENTION_ENABLED", "true"), "false"),
		ProcessingSpanSuccessRetention:              spanSuccessRetention,
		ProcessingSpanFailureRetention:              spanFailureRetention,
		ProcessingSpanRetentionBatch:                spanRetentionBatch,
		DocumentRecognitionDriver:                   strings.ToLower(env("DOCUMENT_RECOGNITION_DRIVER", "default")),
		AsynqConcurrency:                            asynqConcurrency,
		DocumentRecognitionConcurrency:              documentRecognitionConcurrency,
		DocumentExtractionConcurrency:               documentExtractionConcurrency,
		AsynqShutdownTimeout:                        asynqShutdownTimeout,
		ParserGRPCAddress:                           env("PARSER_GRPC_ADDR", "127.0.0.1:50051"),
		ParserGRPCMaxMessageSize:                    int(parserMessageSize),
		PaperCatalogProviders:                       paperCatalogProviders,
		PaperCatalogTimeout:                         paperCatalogTimeout,
		PaperCatalogConcurrency:                     paperCatalogConcurrency,
		PaperCatalogProviderTimeouts:                providerTimeouts,
		PaperCatalogProviderSpacings:                providerSpacings,
		PaperCatalogProviderConcurrency:             providerConcurrency,
		PaperCatalogUserAgent:                       env("PAPER_CATALOG_USER_AGENT", "papersManager/1.0"),
		SemanticScholarAPIKey:                       strings.TrimSpace(os.Getenv("SEMANTIC_SCHOLAR_API_KEY")),
		OpenAlexAPIKey:                              strings.TrimSpace(os.Getenv("OPENALEX_API_KEY")),
		CrossrefMailto:                              strings.TrimSpace(os.Getenv("CROSSREF_MAILTO")),
		PaperExtractionTaskTimeout:                  paperExtractionTaskTimeout,
		PaperExtractionTaskMaxRetry:                 paperExtractionTaskMaxRetry,
		PaperCatalogTaskTimeout:                     paperCatalogTaskTimeout,
		PaperCatalogTaskMaxRetry:                    paperCatalogTaskMaxRetry,
		PaperCatalogSourceStopScore:                 paperCatalogSourceStopScore,
		PaperReferenceCatalogMaxPerDocument:         paperReferenceCatalogMaxPerDocument,
		PaperReferenceCatalogConcurrency:            paperReferenceCatalogConcurrency,
		PaperReferenceCatalogPerDocumentConcurrency: paperReferenceCatalogPerDocumentConcurrency,
		PaperReferenceAutoResolve:                   paperReferenceAutoResolve,
		RemotePDFDownloadTimeout:                    remotePDFDownloadTimeout,
		RemotePDFMaxRedirects:                       remotePDFMaxRedirects,
		RemotePDFDownloadConcurrency:                remotePDFDownloadConcurrency,
		RemotePDFHostConcurrency:                    remotePDFHostConcurrency,
		RemotePDFHostSpacing:                        remotePDFHostSpacing,
	}, nil
}

// PaperCatalogTimeoutFor returns a provider-specific budget while preserving
// PAPER_CATALOG_TIMEOUT for existing deployments.
func (c Config) PaperCatalogTimeoutFor(provider string) time.Duration {
	if timeout := c.PaperCatalogProviderTimeouts[strings.ToLower(strings.TrimSpace(provider))]; timeout > 0 {
		return timeout
	}
	return c.PaperCatalogTimeout
}

func (c Config) PaperCatalogSpacingFor(provider string) time.Duration {
	if spacing := c.PaperCatalogProviderSpacings[strings.ToLower(strings.TrimSpace(provider))]; spacing > 0 {
		return spacing
	}
	return time.Second
}

func (c Config) PaperCatalogConcurrencyFor(provider string) int {
	if concurrency := c.PaperCatalogProviderConcurrency[strings.ToLower(strings.TrimSpace(provider))]; concurrency > 0 {
		return concurrency
	}
	return 1
}

func paperCatalogProviderPolicy(providers []string, fallbackTimeout time.Duration) (map[string]time.Duration, map[string]time.Duration, map[string]int, error) {
	timeouts := make(map[string]time.Duration, len(providers))
	spacings := make(map[string]time.Duration, len(providers))
	concurrency := make(map[string]int, len(providers))
	defaults := map[string]struct {
		spacing     time.Duration
		concurrency int
	}{
		"arxiv": {3 * time.Second, 1}, "semantic_scholar": {time.Second, 1},
		"openalex": {time.Second, 1}, "crossref": {500 * time.Millisecond, 1},
	}
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		envKey := strings.ToUpper(strings.NewReplacer("-", "_", ":", "_").Replace(key))
		defaultPolicy, ok := defaults[key]
		if !ok {
			defaultPolicy = struct {
				spacing     time.Duration
				concurrency int
			}{time.Second, 1}
		}
		if key == "semantic_scholar" && strings.TrimSpace(os.Getenv("SEMANTIC_SCHOLAR_API_KEY")) != "" {
			defaultPolicy.spacing = 100 * time.Millisecond
			defaultPolicy.concurrency = 10
		}
		if key == "openalex" && strings.TrimSpace(os.Getenv("OPENALEX_API_KEY")) != "" {
			defaultPolicy.spacing = 100 * time.Millisecond
			defaultPolicy.concurrency = 10
		}
		if key == "crossref" && strings.TrimSpace(os.Getenv("CROSSREF_MAILTO")) != "" {
			defaultPolicy.spacing = 300 * time.Millisecond
			defaultPolicy.concurrency = 3
		}
		var err error
		timeouts[key], err = durationEnv("PAPER_CATALOG_"+envKey+"_TIMEOUT", fallbackTimeout)
		if err != nil {
			return nil, nil, nil, err
		}
		spacings[key], err = durationEnv("PAPER_CATALOG_"+envKey+"_SPACING", defaultPolicy.spacing)
		if err != nil {
			return nil, nil, nil, err
		}
		concurrency[key], err = positiveIntEnv("PAPER_CATALOG_"+envKey+"_CONCURRENCY", defaultPolicy.concurrency, 100)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return timeouts, spacings, concurrency, nil
}

func positiveInt64Env(key string, fallback, maximum int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return parsed, nil
}

func positiveIntEnv(key string, fallback, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || parsed > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return parsed, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", key)
	}
	return duration, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func listEnv(key, fallback string) []string {
	raw := env(key, fallback)
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range strings.Split(raw, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}
