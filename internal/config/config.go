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
	defaultDocumentHousekeepingInterval = 5 * time.Minute
	defaultDocumentStaleThreshold       = time.Hour + 10*time.Minute
	defaultPaperCatalogTimeout          = 10 * time.Second
	defaultPaperCatalogConcurrency      = 4
	defaultPaperExtractionTaskTimeout   = 2 * time.Minute
	defaultPaperExtractionTaskMaxRetry  = 3
	defaultPaperCatalogTaskTimeout      = 10 * time.Minute
	defaultPaperCatalogTaskMaxRetry     = 5
)

// Config contains the infrastructure options selected at process startup.
type Config struct {
	HTTPAddress       string
	DatabaseDriver    string
	DatabaseURL       string
	CORSOrigin        string
	FileStorageDriver string
	UploadDirectory   string
	// RecognitionOutputDirectory is the shared, trusted root where a
	// directory-mode recognizer may create an output package. The API copies
	// only the Markdown file from this directory into durable FileStorage.
	RecognitionOutputDirectory   string
	MaxUploadSize                int64
	AutoMigrate                  bool
	DocumentHousekeepingEnabled  bool
	DocumentHousekeepingInterval time.Duration
	DocumentStaleThreshold       time.Duration
	DocumentRecognitionDriver    string
	ParserGRPCAddress            string
	ParserGRPCMaxMessageSize     int
	PaperCatalogProviders        []string
	PaperCatalogTimeout          time.Duration
	PaperCatalogConcurrency      int
	PaperCatalogUserAgent        string
	SemanticScholarAPIKey        string
	OpenAlexAPIKey               string
	CrossrefMailto               string
	PaperExtractionTaskTimeout   time.Duration
	PaperExtractionTaskMaxRetry  int
	PaperCatalogTaskTimeout      time.Duration
	PaperCatalogTaskMaxRetry     int
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
	paperCatalogProviders := listEnv(
		"PAPER_CATALOG_PROVIDERS",
		"arxiv,semantic_scholar,openalex,crossref",
	)
	if len(paperCatalogProviders) == 0 {
		return Config{}, fmt.Errorf("PAPER_CATALOG_PROVIDERS must contain at least one provider")
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
	return Config{
		HTTPAddress:                  env("HTTP_ADDR", "127.0.0.1:8080"),
		DatabaseDriver:               strings.ToLower(env("DB_DRIVER", "postgres")),
		DatabaseURL:                  env("DATABASE_URL", "postgres://papers:papers@localhost:5432/papers?sslmode=disable"),
		CORSOrigin:                   env("CORS_ORIGIN", "http://localhost:5173"),
		FileStorageDriver:            strings.ToLower(env("FILE_STORAGE_DRIVER", "local")),
		UploadDirectory:              uploadDirectory,
		RecognitionOutputDirectory:   env("RECOGNITION_OUTPUT_DIR", filepath.Join(uploadDirectory, "recognition-output")),
		MaxUploadSize:                maxUploadSize,
		AutoMigrate:                  !strings.EqualFold(env("AUTO_MIGRATE", "true"), "false"),
		DocumentHousekeepingEnabled:  !strings.EqualFold(env("DOCUMENT_HOUSEKEEPING_ENABLED", "true"), "false"),
		DocumentHousekeepingInterval: housekeepingInterval,
		DocumentStaleThreshold:       staleThreshold,
		DocumentRecognitionDriver:    strings.ToLower(env("DOCUMENT_RECOGNITION_DRIVER", "default")),
		ParserGRPCAddress:            env("PARSER_GRPC_ADDR", "127.0.0.1:50051"),
		ParserGRPCMaxMessageSize:     int(parserMessageSize),
		PaperCatalogProviders:        paperCatalogProviders,
		PaperCatalogTimeout:          paperCatalogTimeout,
		PaperCatalogConcurrency:      paperCatalogConcurrency,
		PaperCatalogUserAgent:        env("PAPER_CATALOG_USER_AGENT", "papersManager/1.0"),
		SemanticScholarAPIKey:        strings.TrimSpace(os.Getenv("SEMANTIC_SCHOLAR_API_KEY")),
		OpenAlexAPIKey:               strings.TrimSpace(os.Getenv("OPENALEX_API_KEY")),
		CrossrefMailto:               strings.TrimSpace(os.Getenv("CROSSREF_MAILTO")),
		PaperExtractionTaskTimeout:   paperExtractionTaskTimeout,
		PaperExtractionTaskMaxRetry:  paperExtractionTaskMaxRetry,
		PaperCatalogTaskTimeout:      paperCatalogTaskTimeout,
		PaperCatalogTaskMaxRetry:     paperCatalogTaskMaxRetry,
	}, nil
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
