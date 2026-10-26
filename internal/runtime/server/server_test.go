package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from maintenance_test.go.
// ---------------------------------------------------------------------------

// TestMaintenanceAdmissionOnlyAllowsHealthAndMCPHandshake verifies that readiness
// probes and the MCP handshake pass while ordinary API routes and MCP tool/resource
// calls are refused.
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

// ---------------------------------------------------------------------------
// Consolidated from runtime_test.go.
// ---------------------------------------------------------------------------

// TestRuntimeLifecycle verifies that a runtime starts once, rejects a duplicate start,
// and shuts down idempotently only after its cleanup callback has completed.
func TestRuntimeLifecycle(t *testing.T) {
	stopped := make(chan struct{})
	app := &Runtime{run: func(ctx context.Context, ready func()) error {
		ready()
		<-ctx.Done()
		close(stopped)
		return nil
	}}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err == nil {
		t.Fatal("duplicate start accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Shutdown returned before cleanup")
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestRuntimeStartupFailureAndShutdownDeadline verifies that startup errors propagate
// and that shutdown honors its context deadline when cleanup remains blocked.
func TestRuntimeStartupFailureAndShutdownDeadline(t *testing.T) {
	want := errors.New("startup failed")
	failed := &Runtime{run: func(context.Context, func()) error { return want }}
	if err := failed.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("startup error not propagated: %v", err)
	}
	finish := make(chan struct{})
	blocked := &Runtime{run: func(ctx context.Context, ready func()) error {
		ready()
		<-finish
		return nil
	}}
	if err := blocked.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := blocked.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown deadline ignored: %v", err)
	}
	close(finish)
	if err := blocked.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
