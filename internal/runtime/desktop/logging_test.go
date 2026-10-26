package desktop

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

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
