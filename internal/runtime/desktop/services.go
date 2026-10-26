package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func closeInput(c *child, _ context.Context) {
	_ = c.stdin.Close()
}

func (r *Runtime) serviceEnv(values map[string]string) []string {
	if values == nil {
		values = map[string]string{}
	}
	temporary := filepath.Join(r.cfg.DataDir, "cache", "tmp")
	// PostgreSQL tools may link private libpq. Never resolve it from a
	// developer's database installation after the bundle has been relocated.
	privateLibraries := filepath.Join(r.cfg.PostgresBin, "..", "lib")
	values["LD_LIBRARY_PATH"] = privateLibraries
	values["DYLD_LIBRARY_PATH"] = privateLibraries
	values["TEMP"] = temporary
	values["TMP"] = temporary
	values["TMPDIR"] = temporary
	values["XDG_CACHE_HOME"] = filepath.Join(r.cfg.DataDir, "cache")
	return childEnv(values)
}

func (r *Runtime) parserArgs() []string {
	return []string{
		"-m", "papers_parser.server",
		"--address", address(r.cfg.Ports.Parser),
		"--upload-dir", filepath.Join(r.cfg.DataDir, "uploads"),
		"--execution-mode", "shared",
		"--recognize-concurrency", fmt.Sprint(r.cfg.Concurrency.Recognition),
		"--extract-concurrency", fmt.Sprint(r.cfg.Concurrency.Extraction),
		"--render-concurrency", fmt.Sprint(r.cfg.Concurrency.Render),
		"--max-concurrent-rpcs", fmt.Sprint(r.cfg.Concurrency.RPCs),
		"--paper-extraction-engine", r.cfg.ExtractionEngine,
	}
}

func (r *Runtime) parserEnv() []string {
	return r.serviceEnv(map[string]string{
		"PYTHONPATH": r.cfg.ParserDir, "PYTHONUNBUFFERED": "1",
		"PYTHONDONTWRITEBYTECODE": "1", "PAPERS_PARENT_STDIN": "1",
		"LOG_PATH": filepath.Join(r.cfg.DataDir, "logs", "parser-detail.log"),
	})
}

func (r *Runtime) apiEnv() []string {
	probe := "0"
	if r.maintenanceProbe {
		probe = "1"
	}
	return r.serviceEnv(map[string]string{
		"PAPERS_MAINTENANCE_PROBE":         probe,
		"PAPERS_PARENT_STDIN":              "1",
		"PAPERS_SKIP_DOTENV":               "1",
		"HTTP_ADDR":                        address(r.cfg.Ports.API),
		"MCP_ENABLED":                      "true",
		"MCP_ADDR":                         address(r.cfg.Ports.MCP),
		"MCP_TOKEN":                        r.cfg.MCPToken,
		"DATABASE_URL":                     r.dsn(),
		"REDIS_ADDR":                       address(r.cfg.Ports.Redis),
		"REDIS_PASSWORD":                   r.cfg.RedisPassword,
		"REDIS_DB":                         "0",
		"UPLOAD_DIR":                       filepath.Join(r.cfg.DataDir, "uploads"),
		"RECOGNITION_OUTPUT_DIR":           filepath.Join(r.cfg.DataDir, "recognition-output"),
		"DOCUMENT_STRUCTURE_CACHE_DIR":     filepath.Join(r.cfg.DataDir, "cache", "document-structures"),
		"PARSER_GRPC_ADDR":                 address(r.cfg.Ports.Parser),
		"DOCUMENT_RECOGNITION_DRIVER":      "builtin",
		"WEB_DIST_DIR":                     r.cfg.WebDir,
		"CORS_ORIGIN":                      "http://" + address(r.cfg.Ports.API),
		"LOG_PATH":                         filepath.Join(r.cfg.DataDir, "logs", "api-detail.log"),
		"LOG_LEVEL":                        "info",
		"GIN_MODE":                         "release",
		"AUTO_MIGRATE":                     "true",
		"ASYNQ_CONCURRENCY":                fmt.Sprint(r.cfg.Concurrency.Tasks),
		"DOCUMENT_RECOGNITION_CONCURRENCY": fmt.Sprint(r.cfg.Concurrency.Recognition),
		"DOCUMENT_EXTRACTION_CONCURRENCY":  fmt.Sprint(r.cfg.Concurrency.Extraction),
		"DB_MAX_OPEN_CONNS":                fmt.Sprint(max(48, r.cfg.Concurrency.Tasks+8)),
	})
}

// MCP starts only after the API listener is acquired. Proving its private
// credential works prevents another HTTP service's /health from masquerading
// as our API while the owned process is still retrying a contested bind.
func (r *Runtime) mcpReady(ctx context.Context, client *http.Client) error {
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-03-26","capabilities":{},` +
		`"clientInfo":{"name":"launcher","version":"1"}}}`
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, r.Snapshot().MCPURL, strings.NewReader(initialize),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.cfg.MCPToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("owned MCP readiness returned %d", response.StatusCode)
	}
	var result struct {
		Result struct {
			ServerInfo json.RawMessage `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return fmt.Errorf("invalid MCP readiness response")
	}
	if len(result.Result.ServerInfo) == 0 {
		return fmt.Errorf("MCP readiness response has no server info")
	}
	return nil
}
