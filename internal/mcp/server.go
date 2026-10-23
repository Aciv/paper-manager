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
	"paperMaster/internal/types/interfaces"
)

const (
	serverName    = "papers-manager"
	serverVersion = "0.1.0"
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
	// importSlots bounds concurrent import tool calls. URL imports download
	// synchronously inside the request, so this mirrors the HTTP upload gate.
	importSlots chan struct{}
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
	if spec.SchemaVersion != 1 || spec.Server.Name != serverName {
		return nil, fmt.Errorf("unsupported MCP manifest")
	}
	if spec.Server.ToolNamePrefix != "papers_manager_" {
		return nil, fmt.Errorf("MCP tools must use the papers_manager_ prefix")
	}
	importConcurrency := cfg.MCPImportConcurrency
	if importConcurrency <= 0 {
		importConcurrency = 2
	}
	s := &ToolServer{
		cfg: cfg, documents: documents, reading: reading, notes: notes,
		papers: papers, bases: bases, bibliography: bibliography,
		paperInfo: paperInfo, processing: processing,
		importSlots: make(chan struct{}, importConcurrency),
	}
	registry, err := compileRegistry(spec.Tools, s.toolHandlers())
	if err != nil {
		return nil, err
	}
	s.registry = registry
	s.server = sdk.NewServer(&sdk.Implementation{
		Name: serverName, Title: "papersManager", Version: serverVersion,
	}, &sdk.ServerOptions{
		Instructions: "Use papers_manager_ tools for reading notes and library operations. All note writes require expected_revision. Evidence links and section-meta are consumer-owned Markdown.",
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
	if err := validateMCPAccess(s.cfg.MCPAddress, s.cfg.MCPToken); err != nil {
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

func validateMCPAccess(address, token string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("MCP_ADDR must be host:port: %w", err)
	}
	if strings.TrimSpace(token) != "" {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("MCP_TOKEN is required when MCP_ADDR is not loopback")
	}
	return nil
}
