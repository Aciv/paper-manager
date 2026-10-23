package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
