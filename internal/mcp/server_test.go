package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from parsers_test.go.
// ---------------------------------------------------------------------------

// TestParseSectionMeta verifies that a leading section-metadata comment is
// parsed into its description with no diagnostics.
func TestParseSectionMeta(t *testing.T) {
	markdown := "# 方法\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"对应原文 §3\"}\n-->\n\n正文"
	description, diagnostics := parseSectionMeta(markdown)
	if len(diagnostics) != 0 || description != "对应原文 §3" {
		t.Fatalf("parseSectionMeta() = %q, %v", description, diagnostics)
	}
}

// TestParseSectionMetaIgnoresBodyMarker verifies that a metadata marker appearing
// after body content is ignored rather than applied to the section.
func TestParseSectionMetaIgnoresBodyMarker(t *testing.T) {
	markdown := "# 方法\n\n正文\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"late\"}\n-->"
	description, diagnostics := parseSectionMeta(markdown)
	if description != "" || len(diagnostics) != 0 {
		t.Fatalf("parseSectionMeta() = %q, %v", description, diagnostics)
	}
}

// TestParseSectionMetaReportsMalformedJSON verifies that invalid JSON in the
// section-metadata comment yields a diagnostic and no description.
func TestParseSectionMetaReportsMalformedJSON(t *testing.T) {
	markdown := "# 方法\n\n<!-- papers-manager:section-meta v=1\n{not-json}\n-->"
	description, diagnostics := parseSectionMeta(markdown)
	if description != "" || len(diagnostics) == 0 {
		t.Fatalf("parseSectionMeta() = %q, %v", description, diagnostics)
	}
}

// TestParseSectionMetaBoundsDescription verifies that an over-long description is
// truncated to the rune limit and reported as a diagnostic.
func TestParseSectionMetaBoundsDescription(t *testing.T) {
	description := strings.Repeat("x", sectionDescriptionMaxRunes+1)
	markdown := "# 方法\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"" + description + "\"}\n-->"
	parsed, diagnostics := parseSectionMeta(markdown)
	if len([]rune(parsed)) != sectionDescriptionMaxRunes || len(diagnostics) == 0 {
		t.Fatalf("parseSectionMeta() length=%d diagnostics=%v", len([]rune(parsed)), diagnostics)
	}
}

// TestParseEvidenceHref verifies that a well-formed self evidence link is parsed
// into page, rectangle, block, and section fields without diagnostics.
func TestParseEvidenceHref(t *testing.T) {
	target, diagnostics := parseEvidenceHref("papers-manager://self/evidence?v=1&page=3&rect=0.1,0.2,0.8,0.9&block=00000000-0000-4000-8000-000000000000&section=3")
	if len(diagnostics) != 0 || target.Page != 3 || len(target.Rects) != 1 || target.Section != "3" {
		t.Fatalf("parseEvidenceHref() = %+v, %v", target, diagnostics)
	}
}

// TestParseEvidenceHrefRejectsMalformedTargets verifies that non-self schemes,
// unsupported versions, zero pages, invalid block IDs, and inverted rectangles
// are all rejected with diagnostics.
func TestParseEvidenceHrefRejectsMalformedTargets(t *testing.T) {
	for _, href := range []string{
		"https://example.com/evidence?v=1&page=1",
		"papers-manager://self/evidence?v=2&page=1",
		"papers-manager://self/evidence?v=1&page=0",
		"papers-manager://document/not-a-uuid/evidence?v=1&page=1",
		"papers-manager://self/evidence?v=1&page=1&rect=0.5,0.5,0.1,0.2",
	} {
		_, diagnostics := parseEvidenceHref(href)
		if len(diagnostics) == 0 {
			t.Errorf("parseEvidenceHref(%q) accepted malformed input", href)
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated from registry_test.go.
// ---------------------------------------------------------------------------

// TestCompileRegistryRejectsInvalidRegistration verifies that manifest/handler
// mismatches, duplicates, and invalid schemas fail compilation with the expected
// error instead of producing a usable registry.
func TestCompileRegistryRejectsInvalidRegistration(t *testing.T) {
	handler := jsonTool(func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	item := manifestTool{Name: "test", InputSchema: json.RawMessage(`{"type":"object"}`)}
	tests := []struct {
		name     string
		items    []manifestTool
		handlers map[string]toolHandler
		want     string
	}{
		{"missing", []manifestTool{item}, nil, "missing MCP handler"},
		{"nil", []manifestTool{item}, map[string]toolHandler{"test": nil}, "missing MCP handler"},
		{"extra", nil, map[string]toolHandler{"test": handler}, "no manifest entry"},
		{"duplicate", []manifestTool{item, item}, map[string]toolHandler{"test": handler}, "duplicate MCP tool"},
		{"invalid schema JSON", []manifestTool{{Name: "test", InputSchema: json.RawMessage("{")}}, map[string]toolHandler{"test": handler}, "decode schema"},
		{"invalid schema reference", []manifestTool{{Name: "test", InputSchema: json.RawMessage(`{"$ref":"#/$defs/missing"}`)}}, map[string]toolHandler{"test": handler}, "resolve schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileRegistry(tt.items, tt.handlers)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %s", err, tt.want)
			}
		})
	}
}

// TestInvokeValidatesBeforeDispatch verifies that malformed or schema-invalid
// arguments and unknown tools return errors without reaching the handler, while
// a valid call dispatches exactly once.
func TestInvokeValidatesBeforeDispatch(t *testing.T) {
	calls := 0
	registry, err := compileRegistry([]manifestTool{{
		Name: "test", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
	}}, map[string]toolHandler{"test": jsonTool(func(context.Context, json.RawMessage) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	server := &ToolServer{registry: registry}
	for _, raw := range []string{"{", "null", "[]", "{}", `{"id":1}`} {
		result, err := server.Invoke(context.Background(), "test", json.RawMessage(raw))
		if err != nil || !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
			t.Fatalf("Invoke(%s) = %v, %v", raw, result, err)
		}
	}
	result, err := server.Invoke(context.Background(), "unknown", nil)
	if err != nil || !strings.Contains(resultText(t, result), "NOT_FOUND") {
		t.Fatalf("unknown: %v, %v", result, err)
	}
	if calls != 0 {
		t.Fatalf("invalid calls reached handler: %d", calls)
	}
	result, err = server.Invoke(context.Background(), "test", json.RawMessage(`{"id":"x"}`))
	if err != nil || result.IsError || calls != 1 {
		t.Fatalf("valid: %v, %v; calls=%d", result, err, calls)
	}
}

// TestInvokePreservesNativeResultsAndErrors verifies that native tool results
// pass through unchanged with the context forwarded, while handler errors become
// sanitized tool errors that hide internal details.
func TestInvokePreservesNativeResultsAndErrors(t *testing.T) {
	native := &sdk.CallToolResult{Content: []sdk.Content{&sdk.ImageContent{Data: []byte("image"), MIMEType: "image/png"}}}
	key := struct{}{}
	ctx := context.WithValue(context.Background(), key, "value")
	handlers := map[string]toolHandler{
		"image": func(got context.Context, raw json.RawMessage) (*sdk.CallToolResult, error) {
			if got.Value(key) != "value" || string(raw) != "{}" {
				t.Error("context or empty arguments not forwarded")
			}
			return native, nil
		},
		"error": jsonTool(func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("private internal details")
		}),
	}
	items := []manifestTool{
		{Name: "image", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "error", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	registry, err := compileRegistry(items, handlers)
	if err != nil {
		t.Fatal(err)
	}
	server := &ToolServer{registry: registry}
	result, err := server.Invoke(ctx, "image", nil)
	if err != nil || result != native {
		t.Fatalf("native result changed: %v, %v", result, err)
	}
	result, err = server.Invoke(ctx, "error", nil)
	if err != nil || !result.IsError || strings.Contains(resultText(t, result), "private internal details") {
		t.Fatalf("error conversion: %v, %v", result, err)
	}
}

// TestInvokeMatchesSDK verifies that a direct Invoke returns the same canonical
// JSON as an SDK client call, ignoring transport-owned metadata.
func TestInvokeMatchesSDK(t *testing.T) {
	server := newTestServer(t)
	server.notes = &fakeNoteService{revision: 1}
	client := connectTestClient(t, server)
	args := map[string]any{"document_id": "4f13ef47-81b2-4a76-b3bf-97182cfcc868"}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	local, err := server.Invoke(context.Background(), "papers_manager_reading_note_outline", raw)
	if err != nil || local.IsError {
		t.Fatalf("local invocation: %v, %v", local, err)
	}
	remote := callTool(t, client, "papers_manager_reading_note_outline", args)
	// The SDK serializes structured content; compare canonical JSON rather
	// than Go concrete types on opposite sides of the transport.
	localJSON, err := json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	remoteJSON, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	var localValue, remoteValue any
	if err := json.Unmarshal(localJSON, &localValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(remoteJSON, &remoteValue); err != nil {
		t.Fatal(err)
	}
	// Transport-owned metadata is intentionally absent from a local call.
	delete(remoteValue.(map[string]any), "_meta")
	delete(remoteValue.(map[string]any), "resultType")
	if !reflect.DeepEqual(localValue, remoteValue) {
		t.Fatalf("local/SDK mismatch: %s / %s", localJSON, remoteJSON)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from server_test.go.
// ---------------------------------------------------------------------------

func newTestServer(t *testing.T) *ToolServer {
	t.Helper()
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
	}, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewToolServer() error = %v", err)
	}
	return server
}

func connectTestClient(t *testing.T, server *ToolServer) *sdk.ClientSession {
	t.Helper()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

// TestEmbeddedManifestMatchesDocs verifies that the embedded MCP manifest is
// identical to docs/mcp/papers-manager-tools.json.
func TestEmbeddedManifestMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/mcp/papers-manager-tools.json")
	if err != nil {
		t.Fatalf("read docs manifest: %v", err)
	}
	var embedded, published any
	if err := json.Unmarshal(manifestJSON, &embedded); err != nil {
		t.Fatalf("decode embedded manifest: %v", err)
	}
	if err := json.Unmarshal(docs, &published); err != nil {
		t.Fatalf("decode docs manifest: %v", err)
	}
	if !reflect.DeepEqual(embedded, published) {
		t.Fatal("embedded MCP manifest differs from docs/mcp/papers-manager-tools.json")
	}
}

// TestToolServerListsAllPrefixedTools verifies that the server advertises
// exactly 47 tools and that every tool name carries the papers_manager_ prefix.
func TestToolServerListsAllPrefixedTools(t *testing.T) {
	server := newTestServer(t)
	client := connectTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(result.Tools) != 47 {
		t.Fatalf("ListTools() count = %d, want 47", len(result.Tools))
	}
	for _, tool := range result.Tools {
		if !strings.HasPrefix(tool.Name, "papers_manager_") {
			t.Errorf("tool %q has no required prefix", tool.Name)
		}
	}
}

// TestToolSchemaRejectsMissingRequiredArgument verifies that calling a tool
// without its required document_id returns an INVALID_ARGUMENT tool error.
func TestToolSchemaRejectsMissingRequiredArgument(t *testing.T) {
	server := newTestServer(t)
	client := connectTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &sdk.CallToolParams{
		Name: "papers_manager_document_structure", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("CallTool() succeeded with missing document_id")
	}
	text := result.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(text, "INVALID_ARGUMENT") {
		t.Fatalf("error = %s, want INVALID_ARGUMENT", text)
	}
}

// TestMCPAddressValidation verifies that valid listen addresses are accepted,
// a portless address is rejected, and only loopback hosts are classified as
// loopback.
func TestMCPAddressValidation(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8090", "0.0.0.0:8090", "localhost:8090"} {
		if _, err := mcpListenHost(address); err != nil {
			t.Fatalf("valid address %q rejected: %v", address, err)
		}
	}
	if _, err := mcpListenHost("no-port"); err == nil {
		t.Fatal("invalid address accepted")
	}
	if !isLoopbackMCPHost("127.0.0.1") || !isLoopbackMCPHost("::1") || !isLoopbackMCPHost("localhost") {
		t.Fatal("loopback hosts were not detected")
	}
	if isLoopbackMCPHost("0.0.0.0") || isLoopbackMCPHost("") || isLoopbackMCPHost("192.168.1.5") {
		t.Fatal("non-loopback host was treated as loopback")
	}
}

// TestMCPHandlerAllowsEmptyToken verifies that an initialize request succeeds
// without an Authorization header when no MCP token is configured.
func TestMCPHandlerAllowsEmptyToken(t *testing.T) {
	server, err := NewToolServer(config.Config{
		MCPAddress: "0.0.0.0:8090", MCPMaxRequestBytes: 1 << 20,
	}, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewToolServer() error = %v", err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty-token status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestMCPHandlerRequiresBearerToken verifies that a configured MCP token rejects
// unauthenticated requests with 401 and accepts the matching bearer token.
func TestMCPHandlerRequiresBearerToken(t *testing.T) {
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPToken: "secret", MCPMaxRequestBytes: 1 << 20,
	}, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewToolServer() error = %v", err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler() error = %v", err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}
	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200; body=%s", authorized.Code, authorized.Body.String())
	}
}

type fakeNoteService struct {
	revision   int64
	manifest   types.DocumentReadingNote
	section    string
	changeOnce bool
}

func (f *fakeNoteService) GetManifest(context.Context, string) (types.DocumentReadingNote, error) {
	note := f.manifest
	note.Revision = f.revision
	return note, nil
}
func (f *fakeNoteService) ReadSection(context.Context, string, string) (types.ReadingNoteSection, error) {
	if f.changeOnce {
		f.revision++
		f.changeOnce = false
	}
	return types.ReadingNoteSection{Revision: f.revision, Markdown: f.section}, nil
}
func (f *fakeNoteService) Export(context.Context, string) (types.ReadingNoteExport, error) {
	return types.ReadingNoteExport{}, nil
}
func (f *fakeNoteService) ApplyOperations(context.Context, string, types.ReadingNoteOperations) (types.ReadingNoteEditResult, error) {
	return types.ReadingNoteEditResult{}, nil
}
func (f *fakeNoteService) ApplyEdits(context.Context, string, types.ReadingNoteEdits) (types.ReadingNoteEditResult, error) {
	return types.ReadingNoteEditResult{}, nil
}
func (f *fakeNoteService) ReplaceSection(context.Context, string, string, int64, string) (types.ReadingNoteEditResult, error) {
	return types.ReadingNoteEditResult{}, nil
}
func (f *fakeNoteService) Put(context.Context, string, int64, string) (types.DocumentReadingNote, error) {
	return types.DocumentReadingNote{}, nil
}
func (f *fakeNoteService) Delete(context.Context, string, int64) error { return nil }

// TestReadingNoteOutlineDetectsRevisionChange verifies that the outline reader
// fails when the note revision changes between the manifest and section reads.
func TestReadingNoteOutlineDetectsRevisionChange(t *testing.T) {
	sectionID := uuid.New()
	notes := &fakeNoteService{
		revision: 4, changeOnce: true,
		section: "# 总结\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"摘要\"}\n-->\n\n正文",
		manifest: types.DocumentReadingNote{
			DocumentID: uuid.New(), Revision: 4,
			Directory: types.ReadingNoteDirectory{SchemaVersion: 1, Sections: []types.ReadingNoteSectionEntry{{
				ID: sectionID, Order: 0, Level: 1, Title: "总结", ContentRevision: 1, SizeBytes: 10,
			}}},
		},
	}
	server := &ToolServer{notes: notes}
	_, err := server.readingNoteOutline(context.Background(), json.RawMessage(`{"document_id":"`+notes.manifest.DocumentID.String()+`"}`))
	if err == nil {
		t.Fatal("readingNoteOutline() accepted a changed revision")
	}
}
