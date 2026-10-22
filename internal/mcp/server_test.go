package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
)

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

func TestToolServerListsAllPrefixedTools(t *testing.T) {
	server := newTestServer(t)
	client := connectTestClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(result.Tools) != 43 {
		t.Fatalf("ListTools() count = %d, want 43", len(result.Tools))
	}
	for _, tool := range result.Tools {
		if !strings.HasPrefix(tool.Name, "papers_manager_") {
			t.Errorf("tool %q has no required prefix", tool.Name)
		}
	}
}

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

func TestMCPAccessRequiresTokenOffLoopback(t *testing.T) {
	if err := validateMCPAccess("127.0.0.1:8090", ""); err != nil {
		t.Fatalf("loopback without token rejected: %v", err)
	}
	if err := validateMCPAccess("0.0.0.0:8090", ""); err == nil {
		t.Fatal("non-loopback without token accepted")
	}
	if err := validateMCPAccess("0.0.0.0:8090", "secret"); err != nil {
		t.Fatalf("non-loopback with token rejected: %v", err)
	}
}

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
