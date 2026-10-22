package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

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
	importedPath string
	retried      string
	cancelled    string
}

func (s *behaviorDocumentService) CreateDocumentFromLocalFile(
	_ context.Context, path, doi string,
) (*types.Document, error) {
	s.importedPath = path
	return &types.Document{ID: uuid.New(), OriginalFilename: filepath.Base(path), DOI: doi}, nil
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
	createdName string
	deletedID   string
	names       []string
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

func TestManifestToolsHaveDispatchCases(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`case "(papers_manager_[^"]+)"`).FindAllStringSubmatch(string(source), -1)
	cases := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		cases[match[1]] = struct{}{}
	}
	var manifest struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, tool := range manifest.Tools {
		if _, ok := cases[tool.Name]; !ok {
			t.Errorf("manifest tool %q has no dispatch case", tool.Name)
		}
	}
	if len(cases) != len(manifest.Tools) {
		t.Errorf("dispatch cases = %d, manifest tools = %d", len(cases), len(manifest.Tools))
	}
}

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
	if documents.importedPath != path {
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
	// Keep the JSON envelope valid even though the image bytes themselves are
	// not part of it.
	var envelope map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
		t.Fatalf("metadata is not JSON: %v", err)
	}
}
