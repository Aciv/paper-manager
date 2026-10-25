package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaintenanceAdmissionOnlyAllowsHealthAndMCPHandshake(t *testing.T) {
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	})
	for _, path := range []string{"/api/document/upload", "/api/document/123/reading-note", "/"} {
		response := httptest.NewRecorder()
		maintenanceAPIHandler(next).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusServiceUnavailable || called != 0 {
			t.Fatal("maintenance admitted a business request")
		}
	}
	response := httptest.NewRecorder()
	maintenanceAPIHandler(next).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusNoContent || called != 1 {
		t.Fatal("health probe was not delegated")
	}
	for _, method := range []string{"tools/call", "resources/read", "tools/list"} {
		response := httptest.NewRecorder()
		body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"` + method + `"}`)
		maintenanceMCPHandler(next).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", body))
		if response.Code != http.StatusServiceUnavailable || called != 1 {
			t.Fatal("maintenance admitted an MCP resource/tool request")
		}
	}
	response = httptest.NewRecorder()
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	maintenanceMCPHandler(next).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", body))
	if response.Code != http.StatusNoContent || called != 2 {
		t.Fatal("real MCP initialize handler was not delegated")
	}
}
