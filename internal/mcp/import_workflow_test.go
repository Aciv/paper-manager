package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appservice "paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestMCPImportOptionsStayAlignedThroughSDK(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first.pdf", "second.pdf"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("%PDF-1.7\n"), 0o600); err != nil {
			t.Fatal(err)
		}
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
	args := map[string]any{
		"paths": []string{"first.pdf", "second.pdf"},
		"options": []any{
			map[string]any{"metadata": map[string]any{"title": "First", "publishedAt": "2024-01-01"},
				"processing": map[string]any{"source_lookup": "skip"}},
			map[string]any{"metadata": map[string]any{"title": "Second"}, "duplicate_metadata": "none"},
		},
	}
	result := callTool(t, client, "papers_manager_library_import_files", args)
	if result.IsError || len(documents.importOptions) != 2 {
		t.Fatalf("import result = %s, options=%#v", resultText(t, result), documents.importOptions)
	}
	if *documents.importOptions[0].Metadata.Title != "First" ||
		documents.importOptions[0].Processing.SourceLookup != types.SourceLookupSkip ||
		*documents.importOptions[1].Metadata.Title != "Second" ||
		documents.importOptions[1].DuplicateMetadata != types.DuplicateMetadataNone {
		t.Fatalf("options were reordered or dropped: %#v", documents.importOptions)
	}
	documents.importError = appservice.ErrDocumentProcessQueue
	single := callTool(t, client, "papers_manager_library_import_file", map[string]any{"path": "first.pdf"})
	if single.IsError || !strings.Contains(resultText(t, single), `"processingQueued":false`) ||
		!strings.Contains(resultText(t, single), documents.lastDocumentID.String()) {
		t.Fatalf("queue outage hid a committed import: %s", resultText(t, single))
	}
	documents.importError = nil
	args["options"] = []any{map[string]any{}}
	result = callTool(t, client, "papers_manager_library_import_files", args)
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") ||
		len(documents.importOptions) != 2 {
		t.Fatalf("misaligned batch reached document service: %s", resultText(t, result))
	}
}

type workflowDocuments struct {
	behaviorDocumentService
	documents []types.Document
}

func (s *workflowDocuments) GetDocumentProjections(
	_ context.Context, query types.DocumentProjectionQuery,
) ([]types.DocumentProjection, error) {
	items := []types.DocumentProjection{}
	for _, id := range query.DocumentIDs {
		for _, document := range s.documents {
			if document.ID.String() == id {
				items = append(items, types.DocumentProjection{Document: document})
			}
		}
	}
	return items, nil
}

func TestMCPWorkflowBatchReadinessAndMissingDocuments(t *testing.T) {
	document := types.Document{
		ID: uuid.New(), SourceState: types.DocumentSourceAvailable,
		MarkdownStatus: types.DocumentStageSucceeded, MetadataExtractionStatus: types.DocumentStageSucceeded,
		SourceLookupStatus: types.DocumentStageSkipped, SourceLookupMode: types.SourceLookupSkip,
		ReferenceLookupStatus: types.DocumentStageSucceeded,
	}
	failed := document
	failed.ID, failed.SourceLookupStatus = uuid.New(), types.DocumentStageFailed
	documents := &workflowDocuments{documents: []types.Document{document, failed}}
	server, err := NewToolServer(config.Config{}, documents, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(processingStatusManyInput{
		DocumentIDs: []string{document.ID.String(), failed.ID.String(), uuid.NewString()},
	})
	output, err := server.processingStatusMany(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	result := output.(processingStatusManyResult)
	if result.Status != "partial" || result.Summary["readingReady"] != 2 ||
		result.Summary["missing"] != 1 || len(result.Items) != 2 || len(result.Items[0].StageStates) != 4 {
		t.Fatalf("workflow summary = %#v", result)
	}
	if result.Items[0].WorkflowStatus != "succeeded" || result.Items[1].WorkflowStatus != "partial" {
		t.Fatalf("document workflow states = %#v", result.Items)
	}
}
