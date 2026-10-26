package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"os"
	appservice "paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from tools_behavior_test.go.
// ---------------------------------------------------------------------------

func callTool(t *testing.T, client *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", name, err)
	}
	return result
}

func resultText(t *testing.T, result *sdk.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	text, ok := result.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("first content = %T, want text", result.Content[0])
	}
	return text.Text
}

type behaviorDocumentService struct {
	interfaces.DocumentService
	importedPath       string
	lastDocumentID     uuid.UUID
	retried            string
	cancelled          string
	batchURLs          []types.DocumentURLImportCommand
	batchPaths         []string
	batchDOIs          []string
	batchStatus        types.DocumentImportItemStatus
	projectionQuery    types.DocumentProjectionQuery
	missingProjections map[uuid.UUID]bool
}

func (s *behaviorDocumentService) GetDocumentProjections(
	_ context.Context, query types.DocumentProjectionQuery,
) ([]types.DocumentProjection, error) {
	s.projectionQuery = query
	items := make([]types.DocumentProjection, 0, len(query.DocumentIDs))
	for _, raw := range query.DocumentIDs {
		id, err := uuid.Parse(raw)
		if err != nil || s.missingProjections[id] {
			continue
		}
		projection := types.DocumentProjection{
			Document: types.Document{ID: id, OriginalFilename: "paper-" + id.String() + ".pdf"},
		}
		if query.IncludeMetadata {
			projection.Metadata = &types.PaperMetadata{
				DocumentID: id, Title: "Title " + id.String(),
				SourceName: "ACM", SourceURL: "https://example.org/" + id.String(),
			}
		}
		if query.IncludeProcessing {
			projection.Processing = &types.DocumentProcessingView{
				DocumentID:  id,
				StageStates: []types.DocumentStageState{},
				Stages:      []types.DocumentProcessingSpan{},
			}
		}
		items = append(items, projection)
	}
	return items, nil
}

func (s *behaviorDocumentService) CreateDocumentFromLocalFile(
	_ context.Context, path, doi string,
) (*types.Document, error) {
	s.importedPath = path
	s.lastDocumentID = uuid.New()
	return &types.Document{ID: s.lastDocumentID, OriginalFilename: filepath.Base(path), DOI: doi}, nil
}

func (s *behaviorDocumentService) batchResult(
	count int, filename func(int) string, label func(int) string, dois []string,
) types.DocumentBatchResult {
	status := s.batchStatus
	if status == "" {
		status = types.DocumentImportItemAccepted
	}
	result := types.DocumentBatchResult{Items: make([]types.DocumentBatchItemResult, 0, count)}
	for index := 0; index < count; index++ {
		id := uuid.New()
		s.lastDocumentID = id
		doi := ""
		if len(dois) != 0 {
			doi = dois[index]
		}
		item := types.DocumentBatchItemResult{
			Index: index, Status: status,
			Document: &types.Document{ID: id, DOI: doi, OriginalFilename: filename(index)},
		}
		item.URL = label(index)
		result.Items = append(result.Items, item)
	}
	return result
}

func (s *behaviorDocumentService) CreateDocumentsFromURLs(
	_ context.Context, commands []types.DocumentURLImportCommand,
) (types.DocumentBatchResult, error) {
	s.batchURLs = append([]types.DocumentURLImportCommand(nil), commands...)
	dois := make([]string, len(commands))
	for index := range commands {
		dois[index] = commands[index].DOI
	}
	return s.batchResult(
		len(commands),
		func(int) string { return "" },
		func(index int) string { return commands[index].URL },
		dois,
	), nil
}

func (s *behaviorDocumentService) CreateDocumentsFromLocalFiles(
	_ context.Context, paths []string, dois []string,
) (types.DocumentBatchResult, error) {
	s.batchPaths = append([]string(nil), paths...)
	s.batchDOIs = append([]string(nil), dois...)
	return s.batchResult(
		len(paths),
		func(index int) string { return filepath.Base(paths[index]) },
		func(int) string { return "" },
		dois,
	), nil
}

func (s *behaviorDocumentService) RetryDocumentProcess(
	_ context.Context, id string,
) (*types.Document, error) {
	s.retried = "auto"
	return &types.Document{ID: uuid.MustParse(id)}, nil
}

func (s *behaviorDocumentService) RetryDocumentParse(
	_ context.Context, id string,
) (*types.Document, error) {
	s.retried = "markdown"
	return &types.Document{ID: uuid.MustParse(id)}, nil
}

func (s *behaviorDocumentService) CancelDocumentParse(
	_ context.Context, id string,
) (*types.Document, error) {
	s.cancelled = "markdown"
	return &types.Document{ID: uuid.MustParse(id)}, nil
}

type behaviorPaperInfoService struct {
	interfaces.PaperInfoService
	retried   string
	cancelled bool
}

func (s *behaviorPaperInfoService) RetryExtraction(context.Context, string) error {
	s.retried = "extraction"
	return nil
}
func (s *behaviorPaperInfoService) RetryCatalogResolution(context.Context, string) error {
	s.retried = "catalog"
	return nil
}
func (s *behaviorPaperInfoService) RetryReferenceResolution(context.Context, string) error {
	s.retried = "reference"
	return nil
}
func (s *behaviorPaperInfoService) Cancel(context.Context, string) error {
	s.cancelled = true
	return nil
}

type behaviorBaseService struct {
	interfaces.DocumentBaseService
	createdName    string
	deletedID      string
	addedBaseID    string
	addedDocument  string
	addedDocuments []string
	names          []string
}

func (s *behaviorBaseService) GetBase(_ context.Context, id string) (*types.DocumentBase, error) {
	return &types.DocumentBase{ID: uuid.MustParse(id)}, nil
}

func (s *behaviorBaseService) AddDocumentToBase(_ context.Context, baseID, documentID string) error {
	s.addedBaseID = baseID
	s.addedDocument = documentID
	s.addedDocuments = append(s.addedDocuments, documentID)
	return nil
}

func (s *behaviorBaseService) ListBases(context.Context) ([]types.DocumentBase, error) {
	return []types.DocumentBase{{ID: uuid.New(), Name: "组一"}}, nil
}
func (s *behaviorBaseService) CreateBase(
	_ context.Context, name, description string,
) (*types.DocumentBase, error) {
	s.createdName = name
	return &types.DocumentBase{ID: uuid.New(), Name: name, Description: description}, nil
}
func (s *behaviorBaseService) UpdateBase(
	_ context.Context, id string, name, description *string,
) (*types.DocumentBase, error) {
	base := &types.DocumentBase{ID: uuid.MustParse(id)}
	if name != nil {
		base.Name = *name
	}
	return base, nil
}
func (s *behaviorBaseService) DeleteBase(_ context.Context, id string) error {
	s.deletedID = id
	return nil
}

type behaviorPaperService struct {
	interfaces.PaperService
	addedRaw string
}

func (s *behaviorPaperService) ListCitations(context.Context, string) ([]types.PaperCitation, error) {
	return []types.PaperCitation{{
		PaperReference: types.PaperReference{ReferenceID: uuid.New(), RawReference: "Ref A"},
	}}, nil
}
func (s *behaviorPaperService) ListCitedBy(context.Context, string) ([]types.PaperCitation, error) {
	return nil, nil
}
func (s *behaviorPaperService) AddCitation(
	_ context.Context, citingDocumentID string, input types.PaperCitationInput,
) (*types.PaperCitation, error) {
	s.addedRaw = input.RawReference
	return &types.PaperCitation{PaperReference: types.PaperReference{
		ReferenceID: uuid.New(), CitingDocumentID: uuid.MustParse(citingDocumentID),
		RawReference: input.RawReference,
	}}, nil
}

type behaviorNoteService struct {
	interfaces.NoteReadingService
	replaced string
	deleted  bool
}

func (s *behaviorNoteService) Put(
	_ context.Context, documentID string, expectedRevision int64, markdown string,
) (types.DocumentReadingNote, error) {
	s.replaced = markdown
	return types.DocumentReadingNote{DocumentID: uuid.MustParse(documentID), Revision: expectedRevision + 1}, nil
}
func (s *behaviorNoteService) Delete(context.Context, string, int64) error {
	s.deleted = true
	return nil
}
func (s *behaviorNoteService) Export(context.Context, string) (types.ReadingNoteExport, error) {
	return types.ReadingNoteExport{DocumentID: uuid.New(), Revision: 3, Markdown: "# Note\n\nbody"}, nil
}
func (s *behaviorNoteService) ReplaceSection(
	_ context.Context, documentID, sectionID string, expectedRevision int64, markdown string,
) (types.ReadingNoteEditResult, error) {
	s.replaced = markdown
	return types.ReadingNoteEditResult{DocumentID: uuid.MustParse(documentID), Revision: expectedRevision + 1}, nil
}

type behaviorReadingService struct {
	interfaces.DocumentReadingService
	image []byte
}

func (s *behaviorReadingService) GetDocumentBlockImage(
	context.Context, string, string,
) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(s.image)), "block.png", nil
}

func (s *behaviorReadingService) RenderPage(
	context.Context, string, int, *types.NormalizedPDFRect, int,
) (*types.RenderedPDFPage, error) {
	return &types.RenderedPDFPage{
		PNG: s.image, ContentType: "image/png", Width: 10, Height: 20,
		PageWidth: 600, PageHeight: 800,
	}, nil
}

// TestPageImageToolEnforcesMCPImageLimit verifies that a rendered page image
// larger than the configured byte limit is rejected with PAYLOAD_TOO_LARGE.
func TestPageImageToolEnforcesMCPImageLimit(t *testing.T) {
	image := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{1}, 32)...)
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPMaxImageBytes: 8,
	}, nil, &behaviorReadingService{image: image}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	result := callTool(t, client, "papers_manager_document_read_page_image", map[string]any{
		"document_id": uuid.NewString(), "page_number": 1,
	})
	if !result.IsError || !strings.Contains(resultText(t, result), "PAYLOAD_TOO_LARGE") {
		t.Fatalf("page image result = %s", resultText(t, result))
	}
}

// TestManifestToolsHaveRegisteredHandlers verifies that every manifest tool has
// a validated handler and schema and that no extra tools are registered.
func TestManifestToolsHaveRegisteredHandlers(t *testing.T) {
	server := newTestServer(t)
	var manifest struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, tool := range manifest.Tools {
		if registered, ok := server.registry[tool.Name]; !ok || registered.handler == nil || registered.schema == nil {
			t.Errorf("manifest tool %q has no validated handler", tool.Name)
		}
	}
	if len(server.registry) != len(manifest.Tools) {
		t.Errorf("registered tools = %d, manifest tools = %d", len(server.registry), len(manifest.Tools))
	}
}

// TestLocalImportToolUsesConfiguredRoot verifies that a relative path resolves
// inside the configured import root while an outside absolute path is rejected
// as FORBIDDEN.
func TestLocalImportToolUsesConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "paper.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	documents := &behaviorDocumentService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPLocalImportRoot: root, MaxUploadSize: 1 << 20,
	}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	result := callTool(t, client, "papers_manager_library_import_file", map[string]any{"path": "paper.pdf"})
	if result.IsError {
		t.Fatalf("import failed: %s", resultText(t, result))
	}
	if !sameTestFile(documents.importedPath, path) {
		t.Fatalf("imported path = %q, want %q", documents.importedPath, path)
	}
	if !strings.Contains(resultText(t, result), "paper.pdf") {
		t.Fatalf("result = %s", resultText(t, result))
	}

	escape := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(escape, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = callTool(t, client, "papers_manager_library_import_file", map[string]any{"path": escape})
	if !result.IsError || !strings.Contains(resultText(t, result), "FORBIDDEN") {
		t.Fatalf("escaped path result = %s", resultText(t, result))
	}
}

// TestLocalImportToolAttachesBase verifies that importing a file with base_id
// adds the new document to that base and reports baseId in the result.
func TestLocalImportToolAttachesBase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	documents := &behaviorDocumentService{}
	bases := &behaviorBaseService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPLocalImportRoot: root, MaxUploadSize: 1 << 20, MCPImportConcurrency: 2,
	}, documents, nil, nil, nil, bases, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	baseID := uuid.NewString()
	result := callTool(t, client, "papers_manager_library_import_file", map[string]any{
		"path": "paper.pdf", "base_id": baseID,
	})
	if result.IsError {
		t.Fatalf("import with base failed: %s", resultText(t, result))
	}
	if bases.addedBaseID != baseID {
		t.Fatalf("base id = %q, want %q", bases.addedBaseID, baseID)
	}
	if bases.addedDocument != documents.lastDocumentID.String() {
		t.Fatalf("base document = %q, want %q", bases.addedDocument, documents.lastDocumentID)
	}
	if !strings.Contains(resultText(t, result), "baseId") {
		t.Fatalf("result = %s", resultText(t, result))
	}
}

// TestImportToolRejectsWhenSaturated verifies that an import is rejected with
// RESOURCE_EXHAUSTED while the import concurrency gate is occupied.
func TestImportToolRejectsWhenSaturated(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPLocalImportRoot: root, MaxUploadSize: 1 << 20, MCPImportConcurrency: 1,
	}, &behaviorDocumentService{}, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.importSlots <- struct{}{}
	defer func() { <-server.importSlots }()
	client := connectTestClient(t, server)
	result := callTool(t, client, "papers_manager_library_import_file", map[string]any{"path": "paper.pdf"})
	if !result.IsError || !strings.Contains(resultText(t, result), "RESOURCE_EXHAUSTED") {
		t.Fatalf("saturated import result = %s", resultText(t, result))
	}
}

// TestProcessingRetryAndCancelTools verifies that retry targets route to the
// document or paper-info service and that paper_info cancellation dispatches.
func TestProcessingRetryAndCancelTools(t *testing.T) {
	documents := &behaviorDocumentService{}
	info := &behaviorPaperInfoService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
	}, documents, nil, nil, nil, nil, nil, info, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	documentID := uuid.NewString()
	if result := callTool(t, client, "papers_manager_processing_retry", map[string]any{
		"document_id": documentID, "target": "markdown",
	}); result.IsError {
		t.Fatalf("retry failed: %s", resultText(t, result))
	}
	if documents.retried != "markdown" {
		t.Fatalf("retried = %q", documents.retried)
	}
	if result := callTool(t, client, "papers_manager_processing_retry", map[string]any{
		"document_id": documentID, "target": "reference",
	}); result.IsError {
		t.Fatalf("reference retry failed: %s", resultText(t, result))
	}
	if info.retried != "reference" {
		t.Fatalf("paper info retried = %q", info.retried)
	}
	if result := callTool(t, client, "papers_manager_processing_cancel", map[string]any{
		"document_id": documentID, "target": "paper_info",
	}); result.IsError {
		t.Fatalf("cancel failed: %s", resultText(t, result))
	}
	if !info.cancelled {
		t.Fatal("paper info cancel was not called")
	}
}

// TestBaseAndCitationCollectionsDispatch verifies that base list/create/delete
// and citation list/add tools dispatch to their services with the supplied
// arguments.
func TestBaseAndCitationCollectionsDispatch(t *testing.T) {
	bases := &behaviorBaseService{}
	papers := &behaviorPaperService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
	}, nil, nil, nil, papers, bases, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	baseID := uuid.NewString()
	if result := callTool(t, client, "papers_manager_base_list", map[string]any{}); result.IsError {
		t.Fatalf("base list failed: %s", resultText(t, result))
	}
	if result := callTool(t, client, "papers_manager_base_create", map[string]any{
		"name": "新组",
	}); result.IsError {
		t.Fatalf("base create failed: %s", resultText(t, result))
	}
	if bases.createdName != "新组" {
		t.Fatalf("created base = %q", bases.createdName)
	}
	if result := callTool(t, client, "papers_manager_base_delete", map[string]any{
		"base_id": baseID, "confirm": true,
	}); result.IsError {
		t.Fatalf("base delete failed: %s", resultText(t, result))
	}
	if bases.deletedID != baseID {
		t.Fatalf("deleted base = %q", bases.deletedID)
	}
	documentID := uuid.NewString()
	if result := callTool(t, client, "papers_manager_citation_list", map[string]any{
		"document_id": documentID,
	}); result.IsError {
		t.Fatalf("citation list failed: %s", resultText(t, result))
	}
	if result := callTool(t, client, "papers_manager_citation_add", map[string]any{
		"document_id": documentID, "raw_reference": "Ref B",
	}); result.IsError {
		t.Fatalf("citation add failed: %s", resultText(t, result))
	}
	if papers.addedRaw != "Ref B" {
		t.Fatalf("added raw = %q", papers.addedRaw)
	}
}

// TestNoteAndBlockImageToolsDispatch verifies that note replace/delete/export
// and block/page image tools dispatch and return image content alongside a JSON
// metadata envelope.
func TestNoteAndBlockImageToolsDispatch(t *testing.T) {
	notes := &behaviorNoteService{}
	reading := &behaviorReadingService{image: []byte("\x89PNG\r\n\x1a\npayload")}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPMaxImageBytes: 1 << 20,
	}, nil, reading, notes, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	documentID := uuid.NewString()
	if result := callTool(t, client, "papers_manager_reading_note_replace", map[string]any{
		"document_id": documentID, "markdown": "# 新笔记\n\n正文", "expected_revision": 2,
	}); result.IsError {
		t.Fatalf("note replace failed: %s", resultText(t, result))
	}
	if notes.replaced != "# 新笔记\n\n正文" {
		t.Fatalf("replaced markdown = %q", notes.replaced)
	}
	if result := callTool(t, client, "papers_manager_reading_note_delete", map[string]any{
		"document_id": documentID, "expected_revision": 3, "confirm": true,
	}); result.IsError {
		t.Fatalf("note delete failed: %s", resultText(t, result))
	}
	if !notes.deleted {
		t.Fatal("note delete was not called")
	}
	result := callTool(t, client, "papers_manager_reading_note_export", map[string]any{
		"document_id": documentID,
	})
	if result.IsError || !strings.Contains(resultText(t, result), "body") {
		t.Fatalf("note export result = %s", resultText(t, result))
	}
	result = callTool(t, client, "papers_manager_document_read_block_image", map[string]any{
		"document_id": documentID, "block_id": uuid.NewString(),
	})
	if result.IsError || len(result.Content) != 2 {
		t.Fatalf("block image result = %#v", result)
	}
	image, ok := result.Content[1].(*sdk.ImageContent)
	if !ok || image.MIMEType != "image/png" || len(image.Data) == 0 {
		t.Fatalf("image content = %#v", result.Content[1])
	}
	result = callTool(t, client, "papers_manager_document_read_page_image", map[string]any{
		"document_id": documentID, "page_number": 1,
		"bbox": map[string]any{"x0": 0.1, "y0": 0.2, "x1": 0.8, "y1": 0.9},
		"dpi":  144,
	})
	if result.IsError || len(result.Content) != 2 {
		t.Fatalf("page image result = %#v", result)
	}
	if content, ok := result.Content[1].(*sdk.ImageContent); !ok ||
		content.MIMEType != "image/png" || len(content.Data) == 0 {
		t.Fatalf("page image content = %#v", result.Content[1])
	}
	// Keep the JSON envelope valid even though the image bytes themselves are
	// not part of it.
	var envelope map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
		t.Fatalf("metadata is not JSON: %v", err)
	}
}

// TestBatchURLImportToolReturnsPerItemResultsAndAttachesBase verifies that URL
// batch import forwards DOIs, attaches every imported item to the base, and
// returns per-item results.
func TestBatchURLImportToolReturnsPerItemResultsAndAttachesBase(t *testing.T) {
	documents := &behaviorDocumentService{}
	bases := &behaviorBaseService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPImportConcurrency: 2, BatchImportTimeout: time.Minute,
	}, documents, nil, nil, nil, bases, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	baseID := uuid.NewString()
	result := callTool(t, client, "papers_manager_library_import_urls", map[string]any{
		"urls": []string{
			"https://example.org/a.pdf",
			"https://example.org/b.pdf",
		},
		"dois":    []string{"10.1000/a", ""},
		"base_id": baseID,
	})
	if result.IsError {
		t.Fatalf("batch import failed: %s", resultText(t, result))
	}
	if len(documents.batchURLs) != 2 {
		t.Fatalf("batch URLs = %#v", documents.batchURLs)
	}
	if documents.batchURLs[0].DOI != "10.1000/a" || documents.batchURLs[1].DOI != "" {
		t.Fatalf("batch DOIs = %#v", documents.batchURLs)
	}
	if len(bases.addedDocuments) != 2 {
		t.Fatalf("base memberships = %#v, want 2", bases.addedDocuments)
	}
	text := resultText(t, result)
	for _, want := range []string{`"items"`, `"baseId"`, "https://example.org/a.pdf", "https://example.org/b.pdf"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result missing %q: %s", want, text)
		}
	}
}

// TestBatchFileImportToolReturnsPerItemResultsAndAttachesBase verifies that file
// batch import resolves each path, forwards DOIs, attaches every item to the
// base, and returns per-item results.
func TestBatchFileImportToolReturnsPerItemResultsAndAttachesBase(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.pdf", "b.pdf"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("%PDF-1.7\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	documents := &behaviorDocumentService{}
	bases := &behaviorBaseService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPLocalImportRoot: root, MaxUploadSize: 1 << 20, MCPImportConcurrency: 2,
	}, documents, nil, nil, nil, bases, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	baseID := uuid.NewString()
	result := callTool(t, client, "papers_manager_library_import_files", map[string]any{
		"paths":   []string{"a.pdf", "b.pdf"},
		"dois":    []string{"", "10.1000/b"},
		"base_id": baseID,
	})
	if result.IsError {
		t.Fatalf("batch import failed: %s", resultText(t, result))
	}
	if len(documents.batchPaths) != 2 {
		t.Fatalf("batch paths = %#v", documents.batchPaths)
	}
	if !sameTestFile(documents.batchPaths[0], filepath.Join(root, "a.pdf")) ||
		!sameTestFile(documents.batchPaths[1], filepath.Join(root, "b.pdf")) {
		t.Fatalf("resolved paths = %#v", documents.batchPaths)
	}
	if len(documents.batchDOIs) != 2 || documents.batchDOIs[1] != "10.1000/b" {
		t.Fatalf("batch DOIs = %#v", documents.batchDOIs)
	}
	if len(bases.addedDocuments) != 2 {
		t.Fatalf("base memberships = %#v, want 2", bases.addedDocuments)
	}
	text := resultText(t, result)
	for _, want := range []string{`"items"`, `"baseId"`, "a.pdf", "b.pdf"} {
		if !strings.Contains(text, want) {
			t.Fatalf("result missing %q: %s", want, text)
		}
	}
}

// TestBatchFileImportToolRejectsWholeBatchBeforeImport verifies that one path
// outside the import root rejects the entire batch before any document is
// created.
func TestBatchFileImportToolRejectsWholeBatchBeforeImport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	documents := &behaviorDocumentService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPLocalImportRoot: root, MaxUploadSize: 1 << 20, MCPImportConcurrency: 2,
	}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	result := callTool(t, client, "papers_manager_library_import_files", map[string]any{
		"paths": []string{"paper.pdf", outside},
	})
	if !result.IsError || !strings.Contains(resultText(t, result), "FORBIDDEN") {
		t.Fatalf("escaped batch result = %s", resultText(t, result))
	}
	if len(documents.batchPaths) != 0 {
		t.Fatalf("batch started despite invalid path: %#v", documents.batchPaths)
	}
}

// TestBatchImportToolsEnforceItemAndDOILimits verifies that a batch over the
// 20-item bound and a DOI list misaligned with the URLs are both rejected with
// INVALID_ARGUMENT before dispatch.
func TestBatchImportToolsEnforceItemAndDOILimits(t *testing.T) {
	documents := &behaviorDocumentService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPImportConcurrency: 2,
	}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)

	urls := make([]string, 21)
	for index := range urls {
		urls[index] = "https://example.org/" + uuid.NewString() + ".pdf"
	}
	result := callTool(t, client, "papers_manager_library_import_urls", map[string]any{"urls": urls})
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
		t.Fatalf("over-limit batch result = %s", resultText(t, result))
	}

	result = callTool(t, client, "papers_manager_library_import_urls", map[string]any{
		"urls": []string{"https://example.org/a.pdf", "https://example.org/b.pdf"},
		"dois": []string{"10.1000/a"},
	})
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
		t.Fatalf("misaligned DOI batch result = %s", resultText(t, result))
	}
	if len(documents.batchURLs) != 0 {
		t.Fatalf("batch started despite invalid input: %#v", documents.batchURLs)
	}
}

func toolData(t *testing.T, result *sdk.CallToolResult) map[string]any {
	t.Helper()
	var envelope struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
		t.Fatalf("tool result is not JSON: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("tool result is an error: %s", resultText(t, result))
	}
	var data map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("tool data is not JSON: %v", err)
	}
	return data
}

// TestLibraryGetManyReturnsMetadataAndMissingIDs verifies that a batch read
// deduplicates IDs, requests metadata and processing, and reports unresolvable
// IDs separately as missing.
func TestLibraryGetManyReturnsMetadataAndMissingIDs(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	documents := &behaviorDocumentService{
		missingProjections: map[uuid.UUID]bool{second: true},
	}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
	}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	result := callTool(t, client, "papers_manager_library_get_many", map[string]any{
		"document_ids":       []string{first.String(), second.String(), first.String()},
		"include_processing": true,
	})
	if result.IsError {
		t.Fatalf("batch read failed: %s", resultText(t, result))
	}
	if !documents.projectionQuery.IncludeMetadata || !documents.projectionQuery.IncludeProcessing {
		t.Fatalf("projection flags = %#v", documents.projectionQuery)
	}
	if len(documents.projectionQuery.DocumentIDs) != 2 {
		t.Fatalf("requested ids = %#v, want deduplicated", documents.projectionQuery.DocumentIDs)
	}
	data := toolData(t, result)
	items, _ := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one resolved document", data["items"])
	}
	item, _ := items[0].(map[string]any)
	metadata, _ := item["paperMetadata"].(map[string]any)
	if metadata["title"] != "Title "+first.String() {
		t.Fatalf("metadata = %#v", metadata)
	}
	if _, ok := item["processing"].(map[string]any); !ok {
		t.Fatalf("processing = %#v", item["processing"])
	}
	missing, _ := data["missingDocumentIds"].([]any)
	if len(missing) != 1 || missing[0] != second.String() {
		t.Fatalf("missing ids = %#v", data["missingDocumentIds"])
	}
}

// TestProcessingStatusManyReturnsViews verifies that batch status reads request
// processing data only and return a stage-state view for every found document.
func TestProcessingStatusManyReturnsViews(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	documents := &behaviorDocumentService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
	}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	result := callTool(t, client, "papers_manager_processing_status_many", map[string]any{
		"document_ids": []string{first.String(), second.String()},
	})
	if result.IsError {
		t.Fatalf("batch status failed: %s", resultText(t, result))
	}
	if documents.projectionQuery.IncludeMetadata || !documents.projectionQuery.IncludeProcessing {
		t.Fatalf("projection flags = %#v", documents.projectionQuery)
	}
	data := toolData(t, result)
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", data["items"])
	}
	for index, raw := range items {
		item, _ := raw.(map[string]any)
		if item["documentId"] == nil || item["stageStates"] == nil {
			t.Fatalf("item %d = %#v", index, item)
		}
	}
	if _, present := data["missingDocumentIds"]; present {
		t.Fatalf("unexpected missing ids: %#v", data["missingDocumentIds"])
	}
}

// TestBatchReadToolsEnforceIDLimits verifies that batch reads reject too many
// IDs, non-UUID IDs, and empty ID lists with INVALID_ARGUMENT before calling the
// service.
func TestBatchReadToolsEnforceIDLimits(t *testing.T) {
	documents := &behaviorDocumentService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
	}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)

	ids := make([]string, types.MaxDocumentProjectionIDs+1)
	for index := range ids {
		ids[index] = uuid.NewString()
	}
	result := callTool(t, client, "papers_manager_library_get_many", map[string]any{
		"document_ids": ids,
	})
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
		t.Fatalf("over-limit batch read result = %s", resultText(t, result))
	}

	result = callTool(t, client, "papers_manager_processing_status_many", map[string]any{
		"document_ids": []string{"not-a-uuid"},
	})
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
		t.Fatalf("invalid id result = %s", resultText(t, result))
	}

	result = callTool(t, client, "papers_manager_library_get_many", map[string]any{
		"document_ids": []string{},
	})
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
		t.Fatalf("empty ids result = %s", resultText(t, result))
	}
	if documents.projectionQuery.DocumentIDs != nil {
		t.Fatalf("service was called with invalid input: %#v", documents.projectionQuery)
	}
}

// TestBatchImportAttachesExistingDuplicateToBase verifies that duplicate batch
// items, which resolve to already-available documents, are still attached to the
// requested base and reported with duplicate status.
func TestBatchImportAttachesExistingDuplicateToBase(t *testing.T) {
	documents := &behaviorDocumentService{batchStatus: types.DocumentImportItemDuplicate}
	bases := &behaviorBaseService{}
	server, err := NewToolServer(config.Config{
		MCPAddress: "127.0.0.1:8090", MCPMaxRequestBytes: 8 << 20,
		MCPImportConcurrency: 2,
	}, documents, nil, nil, nil, bases, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	baseID := uuid.NewString()
	result := callTool(t, client, "papers_manager_library_import_urls", map[string]any{
		"urls":    []string{"https://example.org/existing-a.pdf", "https://example.org/existing-b.pdf"},
		"base_id": baseID,
	})
	if result.IsError {
		t.Fatalf("duplicate batch import failed: %s", resultText(t, result))
	}
	if len(bases.addedDocuments) != 2 {
		t.Fatalf("base memberships = %#v, want existing duplicates attached", bases.addedDocuments)
	}
	if !strings.Contains(resultText(t, result), `"status":"duplicate"`) {
		t.Fatalf("result = %s", resultText(t, result))
	}
}

// ---------------------------------------------------------------------------
// Consolidated from tools_extra_test.go.
// ---------------------------------------------------------------------------

// TestResolveLocalImportPathConfinesToRoot verifies that relative paths resolve
// under the root while outside paths and symlink escapes are forbidden and a
// missing file is invalid input.
func TestResolveLocalImportPathConfinesToRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "paper.pdf")
	if err := os.WriteFile(inside, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape.pdf")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	server := &ToolServer{cfg: config.Config{MCPLocalImportRoot: root}}

	got, err := server.resolveLocalImportPath("paper.pdf")
	if err != nil || !sameTestFile(got, inside) {
		t.Fatalf("relative path = %q, err = %v", got, err)
	}
	if _, err := server.resolveLocalImportPath(outside); !errors.Is(err, errLocalImportForbidden) {
		t.Fatalf("outside path error = %v, want forbidden", err)
	}
	if _, err := server.resolveLocalImportPath("escape.pdf"); !errors.Is(err, errLocalImportForbidden) {
		t.Fatalf("symlink escape error = %v, want forbidden", err)
	}
	if _, err := server.resolveLocalImportPath("missing.pdf"); !errors.Is(err, errInvalidToolInput) {
		t.Fatalf("missing file error = %v, want invalid input", err)
	}
}

func sameTestFile(left, right string) bool {
	l, err := os.Stat(left)
	if err != nil {
		return false
	}
	r, err := os.Stat(right)
	return err == nil && os.SameFile(l, r)
}

// TestResolveLocalImportPathDisabled verifies that local imports are rejected
// with errLocalImportDisabled when no import root is configured.
func TestResolveLocalImportPathDisabled(t *testing.T) {
	server := &ToolServer{}
	if _, err := server.resolveLocalImportPath("paper.pdf"); !errors.Is(err, errLocalImportDisabled) {
		t.Fatalf("error = %v, want local import disabled", err)
	}
}

// TestParseToolPublishedAt verifies that date-only and RFC3339 values parse
// while empty, loose, and out-of-range values return errInvalidToolInput.
func TestParseToolPublishedAt(t *testing.T) {
	if _, err := parseToolPublishedAt("2024-03-01"); err != nil {
		t.Fatalf("date parse error = %v", err)
	}
	if _, err := parseToolPublishedAt("2024-03-01T12:00:00Z"); err != nil {
		t.Fatalf("RFC3339 parse error = %v", err)
	}
	for _, value := range []string{"", "March 1", "2024-13-40"} {
		if _, err := parseToolPublishedAt(value); !errors.Is(err, errInvalidToolInput) {
			t.Fatalf("parseToolPublishedAt(%q) error = %v", value, err)
		}
	}
}

// TestClassifyToolErrorMapsInputAndConflictErrors verifies that service and type
// errors map to the documented INVALID_ARGUMENT, CONFLICT, NOT_FOUND, and
// FORBIDDEN tool error codes.
func TestClassifyToolErrorMapsInputAndConflictErrors(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{appservice.ErrInvalidPaperMetadata, "INVALID_ARGUMENT"},
		{types.ErrInvalidPaperReference, "INVALID_ARGUMENT"},
		{types.ErrSelfPaperCitation, "INVALID_ARGUMENT"},
		{appservice.ErrDocumentParseActive, "CONFLICT"},
		{appservice.ErrPaperInfoAttemptActive, "CONFLICT"},
		{types.ErrReferenceDownloadInProgress, "CONFLICT"},
		{types.ErrBibliographyTaskNotFound, "NOT_FOUND"},
		{errors.New("invalid document id: nope"), "INVALID_ARGUMENT"},
		{errLocalImportDisabled, "FORBIDDEN"},
		{errLocalImportForbidden, "FORBIDDEN"},
	}
	for _, test := range cases {
		if got := classifyToolError(test.err).Code; got != test.code {
			t.Errorf("classifyToolError(%v) = %s, want %s", test.err, got, test.code)
		}
	}
}

// TestSuccessImageResultCarriesImageContent verifies that a successful image
// result carries an ok:true JSON metadata block plus the image bytes and MIME
// type.
func TestSuccessImageResultCarriesImageContent(t *testing.T) {
	result := successImageResult(map[string]any{"blockId": "b"}, []byte{1, 2, 3}, "image/png")
	if result.IsError || len(result.Content) != 2 {
		t.Fatalf("image result = %#v", result)
	}
	text, ok := result.Content[0].(*sdk.TextContent)
	if !ok || !strings.Contains(text.Text, `"ok":true`) {
		t.Fatalf("metadata content = %#v", result.Content[0])
	}
	image, ok := result.Content[1].(*sdk.ImageContent)
	if !ok || image.MIMEType != "image/png" || len(image.Data) != 3 {
		t.Fatalf("image content = %#v", result.Content[1])
	}
}

// TestManifestUsesIndependentBaseAndCitationCollections verifies that base and
// citation tools use their own groups, other prefixes map to the expected
// groups, and the base/citation group counts are exactly 10 and 9.
func TestManifestUsesIndependentBaseAndCitationCollections(t *testing.T) {
	var manifest struct {
		Tools []struct {
			Name  string `json:"name"`
			Group string `json:"group"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	groups := map[string]int{}
	for _, tool := range manifest.Tools {
		groups[tool.Group]++
		switch {
		case strings.HasPrefix(tool.Name, "papers_manager_base_"):
			if tool.Group != "base" {
				t.Errorf("%s group = %q, want base", tool.Name, tool.Group)
			}
		case strings.HasPrefix(tool.Name, "papers_manager_citation_"):
			if tool.Group != "citation" {
				t.Errorf("%s group = %q, want citation", tool.Name, tool.Group)
			}
		case strings.HasPrefix(tool.Name, "papers_manager_library_") ||
			strings.HasPrefix(tool.Name, "papers_manager_processing_"):
			if tool.Group != "library" {
				t.Errorf("%s group = %q, want library", tool.Name, tool.Group)
			}
		default:
			if tool.Group != "reading" {
				t.Errorf("%s group = %q, want reading", tool.Name, tool.Group)
			}
		}
	}
	if groups["base"] != 10 || groups["citation"] != 9 {
		t.Fatalf("group counts = %#v", groups)
	}
}
