package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	appservice "paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/types"
)

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

func TestResolveLocalImportPathDisabled(t *testing.T) {
	server := &ToolServer{}
	if _, err := server.resolveLocalImportPath("paper.pdf"); !errors.Is(err, errLocalImportDisabled) {
		t.Fatalf("error = %v, want local import disabled", err)
	}
}

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
