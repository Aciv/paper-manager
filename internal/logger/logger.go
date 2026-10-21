package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	loggerMu      sync.Mutex
	activeLogFile io.WriteCloser
)

const ContextLoggerKey = "logger"

// InitFromEnv configures the process logger after configuration files and
// environment variables have been loaded by the server entry point.
func InitFromEnv() {
	loggerMu.Lock()
	defer loggerMu.Unlock()

	if activeLogFile != nil {
		_ = activeLogFile.Close()
		activeLogFile = nil
	}

	logLevel := getLogLevelFromEnv()

	writer := io.Writer(os.Stdout)
	logPath := resolveLogPathFromEnv()
	if logPath != "" {
		file, err := openLogFile(logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logger: failed to open log file %s: %v\n", logPath, err)
		} else {
			activeLogFile = file
			writer = io.MultiWriter(os.Stdout, file)
		}
	}

	jsonLogger := slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
		AddSource: true,
		Level:     logLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					a.Value = slog.StringValue(t.Format(time.RFC3339))
				}
			}
			if a.Key == slog.SourceKey {
				source := a.Value.Any().(*slog.Source)
				shortFile := source.File
				count := 0
				for i := len(source.File) - 1; i > 0; i-- {
					if source.File[i] == '/' {
						count += 1
					}
					if count >= 2 {
						shortFile = source.File[i+1:]
						break
					}
				}
				return slog.String("source", fmt.Sprintf("%s:%d", shortFile, source.Line))
			}
			return a
		},
	}))

	slog.SetDefault(jsonLogger)

}

// getLogLevelFromEnv 从环境变量读取日志级别配置
func getLogLevelFromEnv() slog.Level {
	// 从环境变量读取LOG_LEVEL配置
	logLevelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))

	// 默认Debug
	switch logLevelStr {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

func resolveLogPathFromEnv() string {
	if logPath := strings.TrimSpace(os.Getenv("LOG_PATH")); logPath != "" {
		return filepath.Clean(logPath)
	}

	// `go test` runs each package's test binary with that package as its
	// working directory. A relative default such as Logs/server.log would
	// therefore create stray Logs directories under internal/... during a
	// normal test run. Tests keep structured logs on stdout; an integration
	// test that needs a file can still set LOG_PATH explicitly.
	if strings.HasSuffix(filepath.Base(os.Args[0]), ".test") {
		return ""
	}

	// This default is intentionally only for interactive/local server runs.
	// Deployments should set an absolute LOG_PATH (or rely on stdout logging)
	// rather than depending on the process working directory.
	return filepath.Join("Logs", "server.log")
}

func openLogFile(logPath string) (io.WriteCloser, error) {
	dir := filepath.Dir(logPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    1, // megabytes
		MaxBackups: 100,
		MaxAge:     28, // days
		Compress:   true,
	}, nil
}

func GetLogger(c context.Context) *slog.Logger {
	if logger := c.Value(ContextLoggerKey); logger != nil {
		return logger.(*slog.Logger)
	}
	return slog.With()
}

// WithRequestID 在日志中添加请求ID
func WithRequestID(c context.Context, requestID string) context.Context {
	return WithAttrs(c, slog.String("request_id", requestID))
}

// WithField 向日志中添加一个字段
func WithAttrs(c context.Context, args ...any) context.Context {
	logger := GetLogger(c).With(args...)
	return context.WithValue(c, ContextLoggerKey, logger)
}

func Debug(c context.Context, msg string, args ...any) {
	log(c, slog.LevelDebug, msg, args...)
}

func DebugWithArgs(ctx context.Context, msg string, args ...any) {
	log(ctx, slog.LevelDebug, msg, args...)
}

func Info(c context.Context, msg string, args ...any) {
	log(c, slog.LevelInfo, msg, args...)
}

func InfoWithArgs(ctx context.Context, msg string, args ...any) {
	log(ctx, slog.LevelInfo, msg, args...)
}

func Warn(c context.Context, msg string, args ...any) {
	log(c, slog.LevelWarn, msg, args...)
}

func WarnWithArgs(ctx context.Context, msg string, args ...any) {
	log(ctx, slog.LevelWarn, msg, args...)
}

func Error(c context.Context, msg string, args ...any) {
	log(c, slog.LevelError, msg, args...)
}

func ErrorWithArgs(ctx context.Context, msg string, args ...any) {
	log(ctx, slog.LevelError, msg, args...)
}

// log writes a record with the program counter of the package that called the
// public logger helper. slog.Logger.Info and its siblings normally capture
// their direct caller, which would otherwise be this package's wrapper and
// make every application log appear to originate from logger/logger.go.
func log(ctx context.Context, level slog.Level, msg string, args ...any) {
	entryLogger := GetLogger(ctx)
	handler := entryLogger.Handler()
	if !handler.Enabled(ctx, level) {
		return
	}

	record := slog.NewRecord(time.Now(), level, msg, callerPC())
	record.Add(args...)
	_ = handler.Handle(ctx, record)
}

// callerPC returns the public helper's caller. The fixed depth is deliberate:
// runtime.Callers -> callerPC -> log -> Debug/Info/Warn/Error -> application.
func callerPC() uintptr {
	var pcs [1]uintptr
	if runtime.Callers(4, pcs[:]) == 0 {
		return 0
	}
	return pcs[0]
}
