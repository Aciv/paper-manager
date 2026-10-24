package desktop

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed management.html
var managementPage string

// Handler is a local management page with no login. Host and same-origin write
// checks prevent other websites from controlling the application's lifecycle.
func (r *Runtime) Handler() http.Handler {
	host := address(r.cfg.Ports.Manager)
	origin := "http://" + host
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if req.Host != host || (req.Header.Get("Origin") != "" &&
			req.Header.Get("Origin") != origin) {
			http.Error(w, "untrusted local management origin", http.StatusForbidden)
			return
		}
		if req.Method != http.MethodGet && req.Header.Get("Origin") != origin {
			http.Error(w, "same-origin management operation required", http.StatusForbidden)
			return
		}
		if req.Method == http.MethodGet {
			switch req.URL.Path {
			case "/":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, managementPage)
				return
			case "/management.js":
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				_, _ = io.WriteString(w, managementJS)
				return
			case "/management.css":
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
				_, _ = io.WriteString(w, managementCSS)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/status":
			_ = json.NewEncoder(w).Encode(r.Snapshot())
		case req.Method == http.MethodGet && req.URL.Path == "/mcp-config":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mcpServers": map[string]any{"papers-manager": map[string]any{
					"type": "http", "url": r.Snapshot().MCPURL,
					"headers": map[string]string{"Authorization": "Bearer " + r.cfg.MCPToken},
				}},
			})
		case req.Method == http.MethodGet && req.URL.Path == "/logs":
			name := req.URL.Query().Get("service")
			switch name {
			case "api", "parser", "postgres", "redis", "initdb":
			default:
				http.Error(w, "unknown service", http.StatusBadRequest)
				return
			}
			content, err := r.logTail(name)
			if err != nil {
				http.Error(w, "log unavailable", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"text": content})
		case req.Method == http.MethodPost && req.URL.Path == "/start":
			if !r.Start() {
				http.Error(w, "services are already running or stopping", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case req.Method == http.MethodPost && req.URL.Path == "/stop":
			r.Stop()
			w.WriteHeader(http.StatusAccepted)
		case req.Method == http.MethodPost && req.URL.Path == "/quit":
			r.Quit()
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, req)
		}
	})
}

func (r *Runtime) logTail(name string) (string, error) {
	file, err := os.Open(filepath.Join(r.cfg.DataDir, "logs", name+".log"))
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	const maxBytes = 64 << 10
	if info.Size() > maxBytes {
		if _, err := file.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return "", err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes))
	return r.cfg.redact(string(data)), err
}

// ManagementURL contains no credential and is stable across restarts.
func (r *Runtime) ManagementURL() string {
	return fmt.Sprintf("http://%s", address(r.cfg.Ports.Manager))
}

//go:embed management.js
var managementJS string

//go:embed management.css
var managementCSS string
