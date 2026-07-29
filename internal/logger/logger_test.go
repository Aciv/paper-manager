package logger

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *recordHandler) WithGroup(string) slog.Handler { return h }

func TestInfoRecordsApplicationCaller(t *testing.T) {
	handler := &recordHandler{}
	ctx := context.WithValue(context.Background(), ContextLoggerKey, slog.New(handler))

	Info(ctx, "source test")

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.records) != 1 {
		t.Fatalf("record count = %d, want 1", len(handler.records))
	}
	frames := runtime.CallersFrames([]uintptr{handler.records[0].PC})
	frame, _ := frames.Next()
	file := frame.File
	if !strings.HasSuffix(file, "logger_test.go") {
		t.Fatalf("record source file = %q, want logger_test.go", file)
	}
}
