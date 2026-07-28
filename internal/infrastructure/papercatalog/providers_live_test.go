package papercatalog

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

const liveCatalogTestEnv = "PAPER_CATALOG_LIVE_TEST"

// TestLivePaperCatalogProviders exercises the real public provider APIs.
//
// It is deliberately opt-in so normal test runs remain deterministic:
//
//	PAPER_CATALOG_LIVE_TEST=1 go test \
//	  ./internal/infrastructure/papercatalog \
//	  -run '^TestLivePaperCatalogProviders$' -v
//
// Optional credentials are read from the same environment variables as the
// application. Never place API keys directly in this file.
func TestLivePaperCatalogProviders(t *testing.T) {
	if os.Getenv(liveCatalogTestEnv) != "1" {
		t.Skip("set PAPER_CATALOG_LIVE_TEST=1 to call real catalog APIs")
	}

	const userAgent = "papersManager-live-test/1.0"
	newClient := func() *http.Client {
		return &http.Client{Timeout: 20 * time.Second}
	}

	arXiv, err := NewArXivProvider(newClient(), userAgent)
	if err != nil {
		t.Fatal(err)
	}
	semanticScholar, err := NewSemanticScholarProvider(
		newClient(),
		os.Getenv("SEMANTIC_SCHOLAR_API_KEY"),
		userAgent,
	)
	if err != nil {
		t.Fatal(err)
	}
	openAlex, err := NewOpenAlexProvider(
		newClient(),
		os.Getenv("OPENALEX_API_KEY"),
		userAgent,
	)
	if err != nil {
		t.Fatal(err)
	}
	crossref, err := NewCrossrefProvider(
		newClient(),
		os.Getenv("CROSSREF_MAILTO"),
		userAgent,
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		provider      interfaces.PaperCatalogProvider
		query         types.PaperLookupQuery
		titleContains string
	}{
		{
			name:     ArXivProvider,
			provider: arXiv,
			query: types.PaperLookupQuery{
				SourceURL: "https://arxiv.org/abs/1706.03762",
				Limit:     3,
			},
			titleContains: "attention is all you need",
		},
		{
			name:     SemanticScholarProvider,
			provider: semanticScholar,
			query: types.PaperLookupQuery{
				DOI:   "10.18653/v1/2020.acl-main.447",
				Limit: 3,
			},
			titleContains: "s2orc",
		},
		{
			name:     OpenAlexProvider,
			provider: openAlex,
			query: types.PaperLookupQuery{
				DOI:   "10.1038/nature14539",
				Limit: 3,
			},
			titleContains: "deep learning",
		},
		{
			name:     CrossrefProvider,
			provider: crossref,
			query: types.PaperLookupQuery{
				DOI:   "10.1038/nature14539",
				Limit: 3,
			},
			titleContains: "deep learning",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()

			candidates, err := test.provider.Search(ctx, test.query)
			if err != nil {
				t.Fatalf("live %s search failed: %v", test.name, err)
			}
			if len(candidates) == 0 {
				t.Fatalf("live %s search returned no candidates", test.name)
			}
			candidate := candidates[0]
			if !strings.Contains(
				strings.ToLower(candidate.Title),
				test.titleContains,
			) {
				t.Fatalf(
					"live %s first title = %q, want text containing %q",
					test.name,
					candidate.Title,
					test.titleContains,
				)
			}
			t.Logf(
				"provider=%s provider_id=%s doi=%s score=%.3f title=%q",
				candidate.Provider,
				candidate.ProviderID,
				candidate.DOI,
				candidate.ProviderScore,
				candidate.Title,
			)
		})
	}
}
