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
	}, nil
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
