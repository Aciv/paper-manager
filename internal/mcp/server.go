// Package mcp is the loopback MCP adapter for papersManager. It shares the
// application services and note gates of the HTTP API; it is not a second
// storage or domain module.
package mcp

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types/interfaces"
)

const (
	serverName    = "papers-manager"
	serverVersion = "1.0.1"
)

//go:embed manifest.json
var manifestJSON []byte

type manifest struct {
	SchemaVersion int            `json:"schemaVersion"`
	Server        manifestServer `json:"server"`
	Tools         []manifestTool `json:"tools"`
}

type manifestServer struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	ToolNamePrefix string `json:"toolNamePrefix"`
}

type manifestTool struct {
	Name        string              `json:"name"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	InputSchema json.RawMessage     `json:"inputSchema"`
	Annotations manifestAnnotations `json:"annotations"`
}

type manifestAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

// ToolServer owns the MCP tool registration and backend services.
type ToolServer struct {
	cfg          config.Config
	documents    interfaces.DocumentService
	reading      interfaces.DocumentReadingService
	notes        interfaces.NoteReadingService
	papers       interfaces.PaperService
	bases        interfaces.DocumentBaseService
	bibliography interfaces.BibliographyService
	paperInfo    interfaces.PaperInfoService
	processing   interfaces.ProcessingViewQuery
	server       *sdk.Server
	registry     map[string]registeredTool
	urlImports   interfaces.URLImportService
}

// NewToolServerWithURLImports wires durable URL acceptance for the application
// while keeping the lightweight constructor usable by independent adapters.
func NewToolServerWithURLImports(
	cfg config.Config, documents interfaces.DocumentService, reading interfaces.DocumentReadingService,
	notes interfaces.NoteReadingService, papers interfaces.PaperService, bases interfaces.DocumentBaseService,
	bibliography interfaces.BibliographyService, paperInfo interfaces.PaperInfoService,
	processing interfaces.ProcessingViewQuery, imports interfaces.URLImportService,
) (*ToolServer, error) {
	server, err := NewToolServer(cfg, documents, reading, notes, papers, bases, bibliography, paperInfo, processing)
	if err == nil {
		server.urlImports = imports
	}
	return server, err
}

// NewToolServer is the production DI constructor. It deliberately accepts the
// same service instances as HTTP; callers must not build a second container.
func NewToolServer(
	cfg config.Config,
	documents interfaces.DocumentService,
	reading interfaces.DocumentReadingService,
	notes interfaces.NoteReadingService,
	papers interfaces.PaperService,
	bases interfaces.DocumentBaseService,
	bibliography interfaces.BibliographyService,
	paperInfo interfaces.PaperInfoService,
	processing interfaces.ProcessingViewQuery,
) (*ToolServer, error) {
	var spec manifest
	if err := json.Unmarshal(manifestJSON, &spec); err != nil {
		return nil, fmt.Errorf("decode MCP manifest: %w", err)
	}
	if spec.SchemaVersion != 1 || spec.Server.Name != serverName || spec.Server.Version != serverVersion {
		return nil, fmt.Errorf("unsupported MCP manifest")
	}
	if spec.Server.ToolNamePrefix != "papers_manager_" {
		return nil, fmt.Errorf("MCP tools must use the papers_manager_ prefix")
	}

	s := &ToolServer{
		cfg: cfg, documents: documents, reading: reading, notes: notes,
		papers: papers, bases: bases, bibliography: bibliography,
		paperInfo: paperInfo, processing: processing,
	}
	registry, err := compileRegistry(spec.Tools, s.toolHandlers())
	if err != nil {
		return nil, err
	}
	s.registry = registry
	budgets := newToolBudgets(cfg)
	assignToolBudgets(spec.Tools, s.registry, budgets)

	s.server = sdk.NewServer(&sdk.Implementation{
		Name: serverName, Title: "paper_manager", Version: serverVersion,
	}, &sdk.ServerOptions{
		Instructions: fmt.Sprintf("Use papers_manager_ tools. Keep one writer per document; note workers write directly and return "+
			"compact saved revisions, not Markdown drafts. Pipeline ready documents when markdown succeeds without "+
			"waiting for the whole import batch or metadata lookup. Default 8 active workers, at most 16 across tasks "+
			"and subject to host limits. Independent in-flight tool budgets: read=%d, write=%d, heavy=%d, import=%d. "+
			"On RESOURCE_EXHAUSTED reduce the affected group concurrency "+
			"and retry with bounded backoff. All note writes require expected_revision. "+
			"Evidence links and section-meta are consumer-owned Markdown.",
			cap(budgets[budgetRead]), cap(budgets[budgetWrite]), cap(budgets[budgetHeavy]), cap(budgets[budgetImport])),
	})
	for _, item := range spec.Tools {
		if !strings.HasPrefix(item.Name, spec.Server.ToolNamePrefix) {
			return nil, fmt.Errorf("tool %q does not use the required prefix", item.Name)
		}
		destructive := item.Annotations.DestructiveHint
		openWorld := item.Annotations.OpenWorldHint
		tool := &sdk.Tool{
			Name: item.Name, Title: item.Title, Description: item.Description,
			InputSchema: item.InputSchema,
			Annotations: &sdk.ToolAnnotations{
				ReadOnlyHint:    item.Annotations.ReadOnlyHint,
				DestructiveHint: &destructive,
				IdempotentHint:  item.Annotations.IdempotentHint,
				OpenWorldHint:   &openWorld,
			},
		}
		registered := s.registry[item.Name]
		s.server.AddTool(tool, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			var raw json.RawMessage
			if req != nil && req.Params != nil {
				raw = req.Params.Arguments
			}
			return registered.invoke(ctx, raw)
		})
	}
	return s, nil
}

// Handler returns the streamable HTTP MCP handler. The API process starts it
// on a separate loopback listener so MCP requests share services and note gates
// without passing through Gin middleware.
func (s *ToolServer) Handler() (http.Handler, error) {
	host, err := mcpListenHost(s.cfg.MCPAddress)
	if err != nil {
		return nil, err
	}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server {
		return s.server
	}, &sdk.StreamableHTTPOptions{
		JSONResponse:        true,
		Stateless:           true,
		SessionTimeout:      30 * time.Minute,
		MaxRequestBodyBytes: s.cfg.MCPMaxRequestBytes,
	})
	token := strings.TrimSpace(s.cfg.MCPToken)
	if token == "" {
		// An empty token disables authentication. That is the default for
		// local use; a non-loopback bind is only warned about so a missing
		// env var cannot take the whole API down (Docker publishes MCP on
		// host loopback even though the container listener binds 0.0.0.0).
		if !isLoopbackMCPHost(host) {
			logger.Warn(context.Background(), "MCP server is listening without a bearer token",
				"address", s.cfg.MCPAddress)
		}
		return handler, nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(strings.TrimPrefix(auth, prefix))), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="papers-manager-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}), nil
}

func mcpListenHost(address string) (string, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", fmt.Errorf("MCP_ADDR must be host:port: %w", err)
	}
	return host, nil
}

func isLoopbackMCPHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
