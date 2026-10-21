package service

import (
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"
)

func TestRenderLocalBibliographyEntryEscapesFieldsAndBuildsStableKey(t *testing.T) {
	published := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	metadata := types.PaperMetadata{
		Title: "Design & Code_100%", Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}},
		PublishedAt: &published, Venue: "UI {Systems}", SourceURL: "https://example.test/a?x=1&y=2",
	}
	if key := bibliographyLocalKeyBase(metadata); key != "lovelace2024design" {
		t.Fatalf("key = %q", key)
	}
	entry, err := renderLocalBibliographyEntry("lovelace2024design", "10.1000/example", metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`@misc{lovelace2024design,`, `Design \& Code\_100\%`,
		`UI \{Systems\}`, `10.1000/example`, `x=1\&y=2`,
	} {
		if !strings.Contains(entry, fragment) {
			t.Fatalf("entry missing %q:\n%s", fragment, entry)
		}
	}
}

func TestRenderLocalBibliographyEntryRequiresTitle(t *testing.T) {
	if _, err := renderLocalBibliographyEntry("missing", "", types.PaperMetadata{}); err == nil {
		t.Fatal("expected missing-title failure")
	}
}
