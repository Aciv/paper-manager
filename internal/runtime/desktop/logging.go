package desktop

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"paperMaster/internal/logger"

	"gopkg.in/natefinch/lumberjack.v2"
)

var consoleMu sync.Mutex

// RunWithProgress describes Launcher-owned work without forwarding service logs.
// Completion means the operation succeeded; a heartbeat only means it is pending.
func RunWithProgress(ctx context.Context, stage string, operation func() error) error {
	return runWithProgress(ctx, stage, 5*time.Second, operation)
}

func runWithProgress(ctx context.Context, stage string, interval time.Duration, operation func() error) error {
	started := time.Now()
	logger.Info(ctx, "正在处理，请稍候", "stage", stage)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				logger.Info(ctx, "仍在处理，请稍候", "stage", stage, "elapsed", time.Since(started).Round(time.Second))
			}
		}
	}()
	err := operation()
	close(stop)
	<-done
	if err == nil {
		logger.Info(ctx, "处理完成", "stage", stage, "elapsed", time.Since(started).Round(time.Millisecond))
	} else {
		logger.Warn(ctx, "处理未完成，请查看后续提示", "stage", stage)
	}
	return err
}

// ConfigureLogging is owned by the Launcher entry point, not business servers.
// File and console output share the same line-based credential redaction.
func ConfigureLogging(cfg Config) (func(), error) {
	directory := filepath.Join(cfg.DataDir, "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("创建 Launcher 日志目录失败: %w", err)
	}
	file := &lumberjack.Logger{
		Filename: filepath.Join(directory, "launcher.log"), MaxSize: 2, MaxBackups: 3,
	}
	previous := slog.Default()
	writer := launcherLogWriter(cfg, file, os.Stdout)
	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return func() {
		slog.SetDefault(previous)
		_ = file.Close()
	}, nil
}

func launcherLogWriter(cfg Config, file, console io.Writer) io.Writer {
	return &redactedWriter{cfg: cfg, out: io.MultiWriter(
		&consoleWriter{name: "launcher", out: console}, file,
	)}
}

type consoleWriter struct {
	name string
	out  io.Writer
}

func (w *consoleWriter) Write(p []byte) (int, error) {
	// One complete redacted line per call; lifecycle goroutines share a console.
	consoleMu.Lock()
	defer consoleMu.Unlock()
	if _, err := fmt.Fprintf(w.out, "[%s] %s", w.name, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
