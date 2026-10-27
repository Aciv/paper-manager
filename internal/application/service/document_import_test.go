package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"
)

func importString(value string) *string { return &value }

func TestNormalizeDocumentImportCopiesAndPromotesMetadata(t *testing.T) {
	authors := []types.PaperAuthor{{Name: " A ", Affiliation: " University "}}
	tags := []string{" ML ", "ml", "PDF"}
	options := types.DocumentImportOptions{
		Metadata: &types.PaperMetadataImport{
			DOI: "https://doi.org/10.1234/EXAMPLE",
			PaperMetadataUpdate: types.PaperMetadataUpdate{
				Title: importString(" Paper "), Authors: &authors, Tags: &tags,
				SourceURL: importString(" https://example.org/paper "),
			},
		},
		Processing: types.DocumentImportProcessing{SourceLookup: types.SourceLookupSkip},
	}
	doi, normalized, err := normalizeDocumentImport("", options)
	if err != nil {
		t.Fatal(err)
	}
	if doi != "10.1234/example" || normalized.Metadata.DOI != doi ||
		normalized.Processing.SourceLookup != types.SourceLookupSkip ||
		normalized.DuplicateMetadata != types.DuplicateMetadataFillMissing {
		t.Fatalf("normalized DOI/options = %q, %#v", doi, normalized)
	}
	metadata := normalized.Metadata
	if *metadata.Title != "Paper" || (*metadata.Authors)[0].Name != "A" ||
		(*metadata.Authors)[0].Affiliation != "University" ||
		strings.Join(*metadata.Tags, ",") != "ml,pdf" ||
		*metadata.MetadataSource != types.PaperMetadataSourceManual {
		t.Fatalf("normalized metadata = %#v", metadata)
	}
	*metadata.Title = "Changed"
	(*metadata.Authors)[0].Name = "Changed"
	(*metadata.Tags)[0] = "changed"
	if *options.Metadata.Title != " Paper " || authors[0].Name != " A " || tags[0] != " ML " ||
		options.Metadata.MetadataSource != nil || options.Metadata.DOI != "https://doi.org/10.1234/EXAMPLE" {
		t.Fatal("normalization mutated the input")
	}
}

func TestNormalizeDocumentImportDOIAndDefaults(t *testing.T) {
	_, defaults, err := normalizeDocumentImport("", types.DocumentImportOptions{})
	if err != nil || defaults.Metadata != nil || defaults.Processing.SourceLookup != types.SourceLookupAuto ||
		defaults.DuplicateMetadata != types.DuplicateMetadataFillMissing {
		t.Fatalf("defaults = %#v, %v", defaults, err)
	}
	for _, test := range []struct {
		top, metadata, expected string
	}{
		{"", "", ""},
		{"doi:10.1234/EXAMPLE", "", "10.1234/example"},
		{"10.1234/example", "https://doi.org/10.1234/EXAMPLE", "10.1234/example"},
	} {
		options := types.DocumentImportOptions{Metadata: &types.PaperMetadataImport{DOI: test.metadata}}
		doi, normalized, err := normalizeDocumentImport(test.top, options)
		if err != nil || doi != test.expected || normalized.Processing.SourceLookup != types.SourceLookupAuto {
			t.Fatalf("normalize = %q, %#v, %v", doi, normalized, err)
		}
	}
	if _, _, err := normalizeDocumentImport("10.1234/first", types.DocumentImportOptions{
		Metadata: &types.PaperMetadataImport{DOI: "10.1234/second"},
	}); !errors.Is(err, types.ErrInvalidDocumentImport) {
		t.Fatalf("DOI conflict error = %v", err)
	}
}

func TestNormalizeDocumentImportRejectsInvalidMetadata(t *testing.T) {
	tooManyAuthors := make([]types.PaperAuthor, 101)
	invalidDate := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, update := range map[string]types.PaperMetadataUpdate{
		"title_bytes":   {Title: importString(strings.Repeat("中", 1366))},
		"invalid_utf8":  {Title: importString(string([]byte{0xff}))},
		"nul":           {Title: importString("A\x00B")},
		"control":       {Abstract: importString("A\x01B")},
		"abstract_size": {Abstract: importString(strings.Repeat("a", (32<<10)+1))},
		"encoded_size":  {Abstract: importString(strings.Repeat("\\", 32<<10))},
		"author_count":  {Authors: &tooManyAuthors},
		"author_name":   {Authors: &[]types.PaperAuthor{{Name: " "}}},
		"affiliation":   {Authors: &[]types.PaperAuthor{{Name: "A", Affiliation: "B\x00C"}}},
		"tags":          {Tags: &[]string{""}},
		"source_url":    {SourceURL: importString("file:///tmp/paper.pdf")},
		"credentials":   {SourceURL: importString("https://user:pass@example.org/paper")},
		"no_host":       {SourceURL: importString("https:///paper")},
		"date":          {PublishedAt: &invalidDate},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := normalizeDocumentImport("", types.DocumentImportOptions{
				Metadata: &types.PaperMetadataImport{PaperMetadataUpdate: update},
			})
			if !errors.Is(err, types.ErrInvalidDocumentImport) {
				t.Fatalf("error = %v, want invalid document import", err)
			}
		})
	}
	for _, doi := range []string{"not a DOI", "10.1234/abc\x00", "10.1234/" + strings.Repeat("a", 256)} {
		if _, _, err := normalizeDocumentImport(doi, types.DocumentImportOptions{}); err == nil {
			t.Fatalf("accepted DOI %q", doi)
		}
	}
	if _, _, err := normalizeDocumentImport("", types.DocumentImportOptions{
		Metadata: &types.PaperMetadataImport{DOI: "10.1234/a" + string([]byte{0xff})},
	}); !errors.Is(err, types.ErrInvalidDocumentImport) {
		t.Fatalf("invalid metadata DOI error = %v", err)
	}
}
