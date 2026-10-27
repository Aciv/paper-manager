package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"paperMaster/internal/config"
)

func TestApplicationInfoSDK(t *testing.T) {
	for _, baseURL := range []string{"https://papers.example/prefix/", "http://127.0.0.1:28453/", ""} {
		server, err := NewToolServer(
			config.Config{WebBaseURL: baseURL, MCPToken: "must-not-leak"},
			nil, nil, nil, nil, nil, nil, nil, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		client := connectTestClient(t, server)
		result := callTool(t, client, "papers_manager_application_info", map[string]any{})
		var envelope struct {
			OK   bool                  `json:"ok"`
			Data applicationInfoResult `json:"data"`
		}
		text := resultText(t, result)
		if err := json.Unmarshal([]byte(text), &envelope); err != nil {
			t.Fatal(err)
		}
		if result.IsError || !envelope.OK || envelope.Data.WebBaseURL != baseURL ||
			envelope.Data.WebAvailable != (baseURL != "") || strings.Contains(text, "must-not-leak") {
			t.Fatalf("application info: %s", text)
		}
		if baseURL != "" && envelope.Data.LibraryURL != baseURL+"#/library" {
			t.Fatalf("deployment prefix lost: %s", text)
		}
		if baseURL == "" && (envelope.Data.LibraryURL != "" || len(envelope.Data.Diagnostics) == 0) {
			t.Fatalf("unknown URL invented: %s", text)
		}
		if server.registry["papers_manager_application_info"].budget != budgetRead {
			t.Fatal("application info must use ordinary read budget")
		}
	}
}
