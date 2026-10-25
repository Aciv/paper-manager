package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func maintenanceAPIHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			http.Error(w, "application maintenance in progress", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func maintenanceMCPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "application maintenance in progress", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			http.Error(w, "invalid maintenance handshake", http.StatusBadRequest)
			return
		}
		var request struct{ Method string }
		if json.Unmarshal(body, &request) != nil || (request.Method != "initialize" &&
			request.Method != "notifications/initialized" && request.Method != "ping") {
			http.Error(w, "MCP tools unavailable during maintenance", http.StatusServiceUnavailable)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
