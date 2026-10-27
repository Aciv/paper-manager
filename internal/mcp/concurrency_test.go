package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type blockingNotes struct {
	interfaces.NoteReadingService
	entered chan struct{}
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (b *blockingNotes) GetManifest(ctx context.Context, _ string) (types.DocumentReadingNote, error) {
	active := b.active.Add(1)
	defer b.active.Add(-1)
	for previous := b.peak.Load(); active > previous; previous = b.peak.Load() {
		if b.peak.CompareAndSwap(previous, active) {
			break
		}
	}
	select {
	case b.entered <- struct{}{}:
	case <-ctx.Done():
		return types.DocumentReadingNote{}, ctx.Err()
	}
	select {
	case <-b.release:
		return types.DocumentReadingNote{Revision: 1}, nil
	case <-ctx.Done():
		return types.DocumentReadingNote{}, ctx.Err()
	}
}

// Both real Streamable HTTP sessions and direct Invoke must consume the same
// read budget. Excess calls fail before entering a service; tools/list remains usable.
func TestToolConcurrencySharedAcrossHTTPClientsAndInvoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	notes := &blockingNotes{entered: make(chan struct{}, 8), release: make(chan struct{})}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20, MCPReadConcurrency: 2,
	}, nil, nil, notes, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	transport := httptest.NewServer(handler)
	defer transport.Close()
	connect := func() *sdk.ClientSession {
		t.Helper()
		client := sdk.NewClient(&sdk.Implementation{Name: "concurrency-test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: transport.URL + "/mcp"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	first, second := connect(), connect()
	name := "papers_manager_reading_note_outline"
	args := map[string]any{"document_id": "4f13ef47-81b2-4a76-b3bf-97182cfcc868"}
	raw, _ := json.Marshal(args)
	type answer struct {
		result *sdk.CallToolResult
		err    error
	}
	done := make(chan answer, 2)
	go func() {
		result, err := server.Invoke(ctx, name, raw)
		done <- answer{result, err}
	}()
	awaitEntry := func() {
		t.Helper()
		select {
		case <-notes.entered:
		case <-ctx.Done():
			t.Fatal("tool did not enter its service")
		}
	}
	awaitEntry()
	go func() {
		result, err := first.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		done <- answer{result, err}
	}()
	awaitEntry()
	// Release blocked handlers before closing the HTTP server on any failure.
	defer cancel()
	for _, call := range []func() (*sdk.CallToolResult, error){
		func() (*sdk.CallToolResult, error) {
			return server.Invoke(ctx, "papers_manager_document_structure", raw)
		},
		func() (*sdk.CallToolResult, error) {
			return second.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		},
	} {
		result, err := call()
		if err != nil || !result.IsError || !strings.Contains(resultText(t, result), "RESOURCE_EXHAUSTED") ||
			!strings.Contains(resultText(t, result), `"retryable":true`) {
			t.Fatalf("expected retryable exhaustion: %v, %v", result, err)
		}
	}
	if notes.active.Load() != 2 || notes.peak.Load() != 2 {
		t.Fatalf("limit exceeded: active=%d peak=%d", notes.active.Load(), notes.peak.Load())
	}
	if _, err := second.ListTools(ctx, nil); err != nil {
		t.Fatalf("tools/list blocked by handler budget: %v", err)
	}
	close(notes.release)
	for range 2 {
		select {
		case value := <-done:
			if value.err != nil || value.result.IsError {
				t.Fatalf("permitted tool failed: %v, %v", value.result, value.err)
			}
		case <-ctx.Done():
			t.Fatal("permitted calls did not finish")
		}
	}
	result, err := second.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("released capacity not reused: %v, %v", result, err)
	}
}

func TestToolConcurrencyReleasesAfterErrorAndCancellation(t *testing.T) {
	type waitKey struct{}
	slots := make(chan struct{}, 1)
	entered := make(chan struct{}, 1)
	registry, err := compileRegistry([]manifestTool{{Name: "test", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		map[string]toolHandler{"test": jsonTool(func(ctx context.Context, _ json.RawMessage) (any, error) {
			entered <- struct{}{}
			if ctx.Value(waitKey{}) == true {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, errors.New("private failure")
		})})
	if err != nil {
		t.Fatal(err)
	}
	tool := registry["test"]
	tool.slots = slots
	registry["test"] = tool
	server := &ToolServer{registry: registry}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), waitKey{}, true))
	done := make(chan struct{})
	go func() { _, _ = server.Invoke(ctx, "test", nil); close(done) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled handler did not release")
	}
	for range 2 {
		result, err := server.Invoke(context.Background(), "test", nil)
		<-entered
		if err != nil || !result.IsError || strings.Contains(resultText(t, result), "RESOURCE_EXHAUSTED") {
			t.Fatalf("failed handler leaked its slot: %v, %v", result, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = server.Invoke(cancelled, "test", nil)
	if len(entered) != 0 || len(slots) != 0 {
		t.Fatal("already cancelled call entered the service or leaked a slot")
	}
}

func TestToolBudgetIsolationAndSingleImportAdmission(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("%PDF-1.7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20, MCPLocalImportRoot: root,
		MCPReadConcurrency: 1, MCPWriteConcurrency: 1, MCPHeavyConcurrency: 1, MCPImportConcurrency: 1,
	}, &behaviorDocumentService{}, nil, &fakeNoteService{revision: 1}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	id := "4f13ef47-81b2-4a76-b3bf-97182cfcc868"
	read := "papers_manager_reading_note_outline"
	write := "papers_manager_reading_note_create"
	heavy := "papers_manager_document_read_markdown"
	local := "papers_manager_library_import_file"
	args := map[string]any{"document_id": id}
	writeArgs := map[string]any{"document_id": id, "expected_revision": 0, "markdown": "# Note\n\nContent"}
	for _, test := range []struct {
		name string
		args map[string]any
	}{{local, map[string]any{"path": "paper.pdf"}},
		{"papers_manager_library_import_files", map[string]any{"paths": []string{"paper.pdf"}}}} {
		result := callTool(t, client, test.name, test.args)
		if result.IsError {
			t.Fatalf("one import consumed its budget twice: %s", resultText(t, result))
		}
	}

	// Saturate heavy work; reads, writes and imports must still succeed.
	heavySlots := server.registry[heavy].slots
	heavySlots <- struct{}{}
	defer func() { <-heavySlots }()
	for _, name := range []string{
		heavy, "papers_manager_reading_note_export", "papers_manager_document_read_block_image",
	} {
		input := args
		if strings.HasSuffix(name, "block_image") {
			input = map[string]any{"document_id": id, "block_id": id}
		}
		result := callTool(t, client, name, input)
		if !result.IsError || !strings.Contains(resultText(t, result), `"group":"heavy"`) {
			t.Fatalf("heavy tools did not share their group: %s", resultText(t, result))
		}
	}
	for _, test := range []struct {
		name string
		args map[string]any
	}{{read, args}, {write, writeArgs}, {local, map[string]any{"path": "paper.pdf"}}} {
		result := callTool(t, client, test.name, test.args)
		if result.IsError {
			t.Fatalf("heavy saturation blocked %s: %s", test.name, resultText(t, result))
		}
	}

	readSlots := server.registry[read].slots
	readSlots <- struct{}{}
	defer func() { <-readSlots }()
	result := callTool(t, client, write, writeArgs)
	if result.IsError {
		t.Fatalf("read saturation blocked write: %s", resultText(t, result))
	}
	writeSlots := server.registry[write].slots
	writeSlots <- struct{}{}
	defer func() { <-writeSlots }()
	result = callTool(t, client, write, writeArgs)
	if !result.IsError || !strings.Contains(resultText(t, result), `"group":"write"`) {
		t.Fatalf("write group did not enforce its limit: %s", resultText(t, result))
	}
	// With every other group full, imports still have their own slot.
	result = callTool(t, client, local, map[string]any{"path": "paper.pdf"})
	if result.IsError {
		t.Fatalf("other budgets blocked import: %s", resultText(t, result))
	}
}
