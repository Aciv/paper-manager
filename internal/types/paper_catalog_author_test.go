package types

import "testing"

// The catalog lookup is keyed by DOI/title/source URL. Authors are optional
// matching hints, so a dirty/empty hint must not reject the whole query.
func TestPaperLookupQueryAllowsEmptyAuthorHints(t *testing.T) {
	query := PaperLookupQuery{
		Title:   "UICopilot: Automating UI Synthesis",
		Authors: []string{"", "  ", "Zhen Li"},
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("query with empty author hints rejected: %v", err)
	}
}
