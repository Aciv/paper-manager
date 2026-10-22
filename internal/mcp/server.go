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

	"github.com/google/jsonschema-go/jsonschema"
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
	schemas      map[string]*jsonschema.Resolved
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
	s := &ToolServer{
		cfg: cfg, documents: documents, reading: reading, notes: notes,
		papers: papers, bases: bases, bibliography: bibliography,
		paperInfo: paperInfo, processing: processing,
		schemas: make(map[string]*jsonschema.Resolved, len(spec.Tools)),
	}
	s.server = sdk.NewServer(&sdk.Implementation{
		Name: serverName, Title: "papersManager", Version: serverVersion,
	}, &sdk.ServerOptions{
		Instructions: "Use papers_manager_ tools for reading notes and library operations. All note writes require expected_revision. Evidence links and section-meta are consumer-owned Markdown.",
	})
	for _, item := range spec.Tools {
		if !strings.HasPrefix(item.Name, spec.Server.ToolNamePrefix) {
			return nil, fmt.Errorf("tool %q does not use the required prefix", item.Name)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(item.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode schema for %s: %w", item.Name, err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("resolve schema for %s: %w", item.Name, err)
		}
		s.schemas[item.Name] = resolved
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
		name := item.Name
		s.server.AddTool(tool, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return s.callTool(ctx, name, req)
		})
	}
	return s, nil
}

func (s *ToolServer) callTool(ctx context.Context, name string, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	raw := json.RawMessage(nil)
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	var args any = map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return failureResult(toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "arguments must be a JSON object"}), nil
		}
	}
	resolved := s.schemas[name]
	if resolved == nil {
		return failureResult(toolFailureDetails{Code: "NOT_FOUND", Message: "unknown tool"}), nil
	}
	if err := resolved.Validate(args); err != nil {
		return failureResult(toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "arguments do not match the tool schema"}), nil
	}

	var data any
	var err error
	switch name {
	case "papers_manager_library_search":
		data, err = s.librarySearch(ctx, raw)
	case "papers_manager_library_get":
		data, err = s.libraryGet(ctx, raw)
	case "papers_manager_library_import_url":
		data, err = s.libraryImportURL(ctx, raw)
	case "papers_manager_library_import_file":
		data, err = s.libraryImportFile(ctx, raw)
	case "papers_manager_library_update_metadata":
		data, err = s.libraryUpdateMetadata(ctx, raw)
	case "papers_manager_library_delete":
		data, err = s.libraryDelete(ctx, raw)
	case "papers_manager_processing_status":
		data, err = s.processingStatus(ctx, raw)
	case "papers_manager_processing_retry":
		data, err = s.processingRetry(ctx, raw)
	case "papers_manager_processing_cancel":
		data, err = s.processingCancel(ctx, raw)
	case "papers_manager_document_structure":
		data, err = s.documentStructure(ctx, raw)
	case "papers_manager_document_read_section":
		data, err = s.documentReadSection(ctx, raw)
	case "papers_manager_document_read_markdown":
		data, err = s.documentReadMarkdown(ctx, raw)
	case "papers_manager_document_read_blocks":
		data, err = s.documentReadBlocks(ctx, raw)
	case "papers_manager_document_read_block_image":
		result, imageErr := s.documentReadBlockImage(ctx, raw)
		if imageErr != nil {
			return errorResult(imageErr), nil
		}
		return result, nil
	case "papers_manager_document_read_page_image":
		result, imageErr := s.documentReadPageImage(ctx, raw)
		if imageErr != nil {
			return errorResult(imageErr), nil
		}
		return result, nil
	case "papers_manager_reading_note_outline":
		data, err = s.readingNoteOutline(ctx, raw)
	case "papers_manager_reading_note_read_section":
		data, err = s.readingNoteReadSection(ctx, raw)
	case "papers_manager_reading_note_create":
		data, err = s.readingNoteCreate(ctx, raw)
	case "papers_manager_reading_note_apply_edits":
		data, err = s.readingNoteApplyEdits(ctx, raw)
	case "papers_manager_reading_note_replace":
		data, err = s.readingNoteReplace(ctx, raw)
	case "papers_manager_reading_note_delete":
		data, err = s.readingNoteDelete(ctx, raw)
	case "papers_manager_reading_note_export":
		data, err = s.readingNoteExport(ctx, raw)
	case "papers_manager_reading_note_replace_section":
		data, err = s.readingNoteReplaceSection(ctx, raw)
	case "papers_manager_evidence_validate":
		data, err = s.evidenceValidate(ctx, raw)
	case "papers_manager_base_list":
		data, err = s.baseList(ctx, raw)
	case "papers_manager_base_get":
		data, err = s.baseGet(ctx, raw)
	case "papers_manager_base_list_documents":
		data, err = s.baseListDocuments(ctx, raw)
	case "papers_manager_base_create":
		data, err = s.baseCreate(ctx, raw)
	case "papers_manager_base_update":
		data, err = s.baseUpdate(ctx, raw)
	case "papers_manager_base_delete":
		data, err = s.baseDelete(ctx, raw)
	case "papers_manager_base_add_document":
		data, err = s.baseAddDocument(ctx, raw)
	case "papers_manager_base_remove_document":
		data, err = s.baseRemoveDocument(ctx, raw)
	case "papers_manager_base_bibliography_generate":
		data, err = s.baseBibliographyGenerate(ctx, raw)
	case "papers_manager_base_bibliography_status":
		data, err = s.baseBibliographyStatus(ctx, raw)
	case "papers_manager_citation_list":
		data, err = s.citationList(ctx, raw)
	case "papers_manager_citation_list_cited_by":
		data, err = s.citationListCitedBy(ctx, raw)
	case "papers_manager_citation_add":
		data, err = s.citationAdd(ctx, raw)
	case "papers_manager_citation_replace":
		data, err = s.citationReplace(ctx, raw)
	case "papers_manager_citation_remove":
		data, err = s.citationRemove(ctx, raw)
	case "papers_manager_citation_update_reference":
		data, err = s.citationUpdateReference(ctx, raw)
	case "papers_manager_citation_resolve":
		data, err = s.citationResolve(ctx, raw)
	case "papers_manager_citation_rematch":
		data, err = s.citationRematch(ctx, raw)
	case "papers_manager_citation_download_pdf":
		data, err = s.citationDownloadPDF(ctx, raw)
	default:
		return failureResult(toolFailureDetails{Code: "NOT_FOUND", Message: "unknown tool"}), nil
	}
	if err != nil {
		return errorResult(err), nil
	}
	return successResult(data), nil
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
