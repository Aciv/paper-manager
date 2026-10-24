package desktop

import (
	"context"
	"path/filepath"
)

func closeInput(c *child, _ context.Context) {
	_ = c.stdin.Close()
}

func (r *Runtime) serviceEnv(values map[string]string) []string {
	if values == nil {
		values = map[string]string{}
	}
	temporary := filepath.Join(r.cfg.DataDir, "cache", "tmp")
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
		"--recognize-concurrency", "4", "--extract-concurrency", "4",
		"--max-concurrent-rpcs", "8",
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
	return r.serviceEnv(map[string]string{
		"PAPERS_PARENT_STDIN":          "1",
		"PAPERS_SKIP_DOTENV":           "1",
		"HTTP_ADDR":                    address(r.cfg.Ports.API),
		"MCP_ENABLED":                  "true",
		"MCP_ADDR":                     address(r.cfg.Ports.MCP),
		"MCP_TOKEN":                    r.cfg.MCPToken,
		"DATABASE_URL":                 r.dsn(),
		"REDIS_ADDR":                   address(r.cfg.Ports.Redis),
		"REDIS_PASSWORD":               r.cfg.RedisPassword,
		"REDIS_DB":                     "0",
		"UPLOAD_DIR":                   filepath.Join(r.cfg.DataDir, "uploads"),
		"RECOGNITION_OUTPUT_DIR":       filepath.Join(r.cfg.DataDir, "recognition-output"),
		"DOCUMENT_STRUCTURE_CACHE_DIR": filepath.Join(r.cfg.DataDir, "cache", "document-structures"),
		"PARSER_GRPC_ADDR":             address(r.cfg.Ports.Parser),
		"DOCUMENT_RECOGNITION_DRIVER":  "builtin",
		"WEB_DIST_DIR":                 r.cfg.WebDir,
		"CORS_ORIGIN":                  "http://" + address(r.cfg.Ports.API),
		"LOG_PATH":                     filepath.Join(r.cfg.DataDir, "logs", "api-detail.log"),
		"LOG_LEVEL":                    "info",
		"GIN_MODE":                     "release",
		"AUTO_MIGRATE":                 "true",
	})
}
