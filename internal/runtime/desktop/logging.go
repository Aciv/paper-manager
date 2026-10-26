package desktop

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

var consoleMu sync.Mutex

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
