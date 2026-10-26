package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLauncherProgressReportsWaitingAndStopsAfterCompletion(t *testing.T) {
	var output bytes.Buffer
	heartbeat := make(chan struct{}, 1)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&progressTestWriter{
		out: &output, heartbeat: heartbeat,
	}, nil)))
	defer slog.SetDefault(previous)
	if err := runWithProgress(context.Background(), "兼容性检查", time.Millisecond, func() error {
		select {
		case <-heartbeat:
			return nil
		case <-time.After(time.Second):
			return errors.New("progress heartbeat was not emitted")
		}
	}); err != nil {
		t.Fatal(err)
	}
	lines := output.String()
	if !strings.Contains(lines, "仍在处理，请稍候") || !strings.Contains(lines, "stage=兼容性检查") ||
		!strings.Contains(lines, "elapsed=") || !strings.Contains(lines, "处理完成") {
		t.Fatalf("missing phase or elapsed progress: %s", lines)
	}
	if strings.LastIndex(lines, "仍在处理，请稍候") > strings.LastIndex(lines, "处理完成") {
		t.Fatal("heartbeat emitted after completion")
	}
}

func TestLauncherProgressDoesNotReportFailureAsCompleted(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		var output bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		failure := errors.New("private operation detail")
		err := runWithProgress(ctx, "数据检查", time.Hour, func() error {
			if canceled {
				cancel()
				return ctx.Err()
			}
			return failure
		})
		cancel()
		slog.SetDefault(previous)
		if err == nil || (!canceled && !errors.Is(err, failure)) ||
			(canceled && !errors.Is(err, context.Canceled)) {
			t.Fatalf("operation error was changed: %v", err)
		}
		if !strings.Contains(output.String(), "处理未完成") || strings.Contains(output.String(), "msg=处理完成") ||
			strings.Contains(output.String(), failure.Error()) {
			t.Fatalf("failure was misreported or private details leaked: %s", output.String())
		}
	}
}

type progressTestWriter struct {
	out       io.Writer
	heartbeat chan struct{}
}

func (w *progressTestWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	if bytes.Contains(p, []byte("仍在处理，请稍候")) {
		select {
		case w.heartbeat <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestLauncherLoggingMirrorsOnlyRedactedLifecycleLines(t *testing.T) {
	cfg := testConfig(t)
	var file, console, childFile bytes.Buffer
	launcher := launcherLogWriter(cfg, &file, &console)
	// Chunked writes must never reveal partial credentials on either output.
	_, _ = io.WriteString(launcher, "failed token="+cfg.MCPToken[:16])
	if console.Len() != 0 || file.Len() != 0 {
		t.Fatal("incomplete line was emitted before redaction")
	}
	_, _ = io.WriteString(launcher, cfg.MCPToken[16:]+"\n")
	if file.String() != "failed token=[redacted]\n" || console.String() != "[launcher] "+file.String() {
		t.Fatal("Launcher file/console output differs or leaks a credential")
	}
	// Subservices retain the file-only writer used in Runtime.run/initdb.
	child := &redactedWriter{cfg: cfg, out: &childFile}
	_, _ = io.WriteString(child, "private parser diagnostic\n")
	if strings.Contains(console.String(), "parser diagnostic") || childFile.Len() == 0 {
		t.Fatal("internal service log was forwarded to the Launcher terminal")
	}
}

func TestLauncherConsoleSerializesConcurrentLifecycleLines(t *testing.T) {
	var output bytes.Buffer
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			writer := &consoleWriter{name: "launcher", out: &output}
			for line := 0; line < 20; line++ {
				_, _ = fmt.Fprintf(writer, "worker=%d line=%d\n", index, line)
			}
		}(worker)
	}
	workers.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 80 {
		t.Fatalf("got %d console lines, want 80", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "[launcher] worker=") || strings.Count(line, "[launcher]") != 1 {
			t.Fatalf("mixed console line: %q", line)
		}
	}
}
