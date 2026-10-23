package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolHandler func(context.Context, json.RawMessage) (*sdk.CallToolResult, error)

type registeredTool struct {
	handler toolHandler
	schema  *jsonschema.Resolved
}

// toolHandlers is built once during construction, never mutated at runtime.
// Add implementations here and their public schemas in manifest.json.
func (s *ToolServer) toolHandlers() map[string]toolHandler {
	return map[string]toolHandler{
		"papers_manager_library_search":               jsonTool(s.librarySearch),
		"papers_manager_library_get":                  jsonTool(s.libraryGet),
		"papers_manager_library_get_many":             jsonTool(s.libraryGetMany),
		"papers_manager_library_import_url":           jsonTool(s.libraryImportURL),
		"papers_manager_library_import_urls":          jsonTool(s.libraryImportURLs),
		"papers_manager_library_import_file":          jsonTool(s.libraryImportFile),
		"papers_manager_library_import_files":         jsonTool(s.libraryImportFiles),
		"papers_manager_library_update_metadata":      jsonTool(s.libraryUpdateMetadata),
		"papers_manager_library_delete":               jsonTool(s.libraryDelete),
		"papers_manager_processing_status":            jsonTool(s.processingStatus),
		"papers_manager_processing_status_many":       jsonTool(s.processingStatusMany),
		"papers_manager_processing_retry":             jsonTool(s.processingRetry),
		"papers_manager_processing_cancel":            jsonTool(s.processingCancel),
		"papers_manager_document_structure":           jsonTool(s.documentStructure),
		"papers_manager_document_read_section":        jsonTool(s.documentReadSection),
		"papers_manager_document_read_markdown":       jsonTool(s.documentReadMarkdown),
		"papers_manager_document_read_blocks":         jsonTool(s.documentReadBlocks),
		"papers_manager_document_read_block_image":    s.documentReadBlockImage,
		"papers_manager_document_read_page_image":     s.documentReadPageImage,
		"papers_manager_reading_note_outline":         jsonTool(s.readingNoteOutline),
		"papers_manager_reading_note_read_section":    jsonTool(s.readingNoteReadSection),
		"papers_manager_reading_note_create":          jsonTool(s.readingNoteCreate),
		"papers_manager_reading_note_apply_edits":     jsonTool(s.readingNoteApplyEdits),
		"papers_manager_reading_note_replace":         jsonTool(s.readingNoteReplace),
		"papers_manager_reading_note_delete":          jsonTool(s.readingNoteDelete),
		"papers_manager_reading_note_export":          jsonTool(s.readingNoteExport),
		"papers_manager_reading_note_replace_section": jsonTool(s.readingNoteReplaceSection),
		"papers_manager_evidence_validate":            jsonTool(s.evidenceValidate),
		"papers_manager_base_list":                    jsonTool(s.baseList),
		"papers_manager_base_get":                     jsonTool(s.baseGet),
		"papers_manager_base_list_documents":          jsonTool(s.baseListDocuments),
		"papers_manager_base_create":                  jsonTool(s.baseCreate),
		"papers_manager_base_update":                  jsonTool(s.baseUpdate),
		"papers_manager_base_delete":                  jsonTool(s.baseDelete),
		"papers_manager_base_add_document":            jsonTool(s.baseAddDocument),
		"papers_manager_base_remove_document":         jsonTool(s.baseRemoveDocument),
		"papers_manager_base_bibliography_generate":   jsonTool(s.baseBibliographyGenerate),
		"papers_manager_base_bibliography_status":     jsonTool(s.baseBibliographyStatus),
		"papers_manager_citation_list":                jsonTool(s.citationList),
		"papers_manager_citation_list_cited_by":       jsonTool(s.citationListCitedBy),
		"papers_manager_citation_add":                 jsonTool(s.citationAdd),
		"papers_manager_citation_replace":             jsonTool(s.citationReplace),
		"papers_manager_citation_remove":              jsonTool(s.citationRemove),
		"papers_manager_citation_update_reference":    jsonTool(s.citationUpdateReference),
		"papers_manager_citation_resolve":             jsonTool(s.citationResolve),
		"papers_manager_citation_rematch":             jsonTool(s.citationRematch),
		"papers_manager_citation_download_pdf":        jsonTool(s.citationDownloadPDF),
	}
}

func jsonTool(handler func(context.Context, json.RawMessage) (any, error)) toolHandler {
	return func(ctx context.Context, raw json.RawMessage) (*sdk.CallToolResult, error) {
		data, err := handler(ctx, raw)
		if err != nil {
			return nil, err
		}
		return successResult(data), nil
	}
}

// compileRegistry rejects drift before the SDK can publish any tools.
func compileRegistry(items []manifestTool, handlers map[string]toolHandler) (map[string]registeredTool, error) {
	registry := make(map[string]registeredTool, len(items))
	for _, item := range items {
		if _, exists := registry[item.Name]; exists {
			return nil, fmt.Errorf("duplicate MCP tool %q", item.Name)
		}
		handler := handlers[item.Name]
		if handler == nil {
			return nil, fmt.Errorf("missing MCP handler for %q", item.Name)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(item.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode schema for %s: %w", item.Name, err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("resolve schema for %s: %w", item.Name, err)
		}
		registry[item.Name] = registeredTool{handler: handler, schema: resolved}
	}
	for name := range handlers {
		if _, exists := registry[name]; !exists {
			return nil, fmt.Errorf("MCP handler %q has no manifest entry", name)
		}
	}
	return registry, nil
}

// Invoke executes a registered tool without an MCP transport. It shares schema
// validation and result/error conversion with SDK calls. The registry is read-only
// after construction; concurrent callers need no registration lock.
func (s *ToolServer) Invoke(ctx context.Context, name string, raw json.RawMessage) (*sdk.CallToolResult, error) {
	tool, ok := s.registry[name]
	if !ok {
		return failureResult(toolFailureDetails{Code: "NOT_FOUND", Message: "unknown tool"}), nil
	}
	return tool.invoke(ctx, raw)
}

func (tool registeredTool) invoke(ctx context.Context, raw json.RawMessage) (*sdk.CallToolResult, error) {
	var args any = map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return failureResult(toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "arguments must be a JSON object"}), nil
		}
	} else {
		raw = json.RawMessage("{}")
	}
	if err := tool.schema.Validate(args); err != nil {
		return failureResult(toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "arguments do not match the tool schema"}), nil
	}
	result, err := tool.handler(ctx, raw)
	if err != nil {
		return errorResult(err), nil
	}
	return result, nil
}
