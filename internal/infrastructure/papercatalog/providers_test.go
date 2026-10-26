package papercatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from providers_test.go.
// ---------------------------------------------------------------------------

// TestSemanticScholarSearchNormalizesResponse verifies that a title search sends
// the bounded query and API key and normalizes DOI, venue, and duplicate topics.
func TestSemanticScholarSearchNormalizesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/graph/v1/paper/search" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("query"); got != "A Paper" {
			t.Fatalf("query = %q", got)
		}
		if got := request.URL.Query().Get("limit"); got != "2" {
			t.Fatalf("limit = %q", got)
		}
		if got := request.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[{
			"paperId":"S2-1",
			"externalIds":{"DOI":"https://doi.org/10.1000/TEST.","CorpusId":12345},
			"url":"https://example.test/paper",
			"title":"  A   Paper  ",
			"abstract":"An abstract",
			"venue":"Proceedings",
			"publicationDate":"2024-02-03",
			"authors":[{"name":"Ada Lovelace"}],
			"journal":{"name":"Journal","volume":"2","pages":"1-4"},
			"fieldsOfStudy":["Computer Science","Computer Science"]
		}]}`)
	}))
	defer server.Close()

	provider, err := newSemanticScholarProvider(
		server.URL,
		server.Client(),
		"test-key",
		"catalog-test/1.0",
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), types.PaperLookupQuery{
		Title: "A-Paper",
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	candidate := candidates[0]
	if candidate.DOI != "10.1000/TEST" || candidate.Title != "A Paper" {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
	if candidate.Venue != "Journal" || len(candidate.Tags) != 1 {
		t.Fatalf("unexpected normalized venue/tags: %#v", candidate)
	}
}

// TestSemanticScholarDOILookup verifies that a DOI-based lookup targets the
// exact paper endpoint and returns a maximum-confidence candidate.
func TestSemanticScholarDOILookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/graph/v1/paper/DOI:10.1000/example" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		_, _ = io.WriteString(writer, `{
			"paperId":"S2-doi",
			"externalIds":{"DOI":"10.1000/example"},
			"title":"Exact DOI Match",
			"authors":[{"name":"Grace Hopper"}]
		}`)
	}))
	defer server.Close()

	provider, err := newSemanticScholarProvider(server.URL, server.Client(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), types.PaperLookupQuery{
		DOI: "https://doi.org/10.1000/example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ProviderScore != 1 {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

// TestOpenAlexSearchNormalizesResponse verifies that OpenAlex search bounds
// pagination and year, omits authors from filters, and reconstructs the abstract
// from the inverted index while keeping institutional affiliations.
func TestOpenAlexSearchNormalizesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/works" || query.Get("search") != "A Paper" {
			t.Fatalf("unexpected request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		if query.Get("per-page") != "3" || query.Get("api_key") != "openalex-key" {
			t.Fatalf("unexpected bounded/auth query: %s", request.URL.RawQuery)
		}
		if !strings.Contains(query.Get("filter"), "publication_year:2024") {
			t.Fatalf("filter = %q", query.Get("filter"))
		}
		if strings.Contains(query.Get("filter"), "Ada Lovelace") ||
			strings.Contains(query.Get("filter"), "raw_author_name") {
			t.Fatalf("author unexpectedly constrained OpenAlex query: %q", query.Get("filter"))
		}
		_, _ = io.WriteString(writer, `{"results":[{
			"id":"https://openalex.org/W123",
			"doi":"https://doi.org/10.1000/openalex",
			"display_name":"A Paper",
			"publication_date":"2024-01-02",
			"language":"EN",
			"authorships":[{
				"author":{"display_name":"Ada Lovelace"},
				"institutions":[{"display_name":"Analytical Engine Institute"}]
			}],
			"primary_location":{
				"landing_page_url":"https://example.test/work",
				"source":{"display_name":"Example Journal"}
			},
			"topics":[{"display_name":"Computing"}],
			"abstract_inverted_index":{"first":[0],"second":[1]}
		}]}`)
	}))
	defer server.Close()

	provider, err := newOpenAlexProvider(
		server.URL,
		server.Client(),
		"openalex-key",
		"catalog-test/1.0",
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), types.PaperLookupQuery{
		Title:         "A Paper",
		Authors:       []string{"Ada Lovelace"},
		PublishedYear: 2024,
		Limit:         3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ProviderID != "W123" ||
		candidate.Abstract != "first second" ||
		candidate.Authors[0].Affiliation != "Analytical Engine Institute" {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
}

// TestCrossrefSearchNormalizesResponse verifies that Crossref search sends the
// bounded bibliographic query and mailto parameter while stripping JATS markup
// and preserving publication metadata.
func TestCrossrefSearchNormalizesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/works" || query.Get("rows") != "4" {
			t.Fatalf("unexpected request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		if query.Get("mailto") != "ops@example.test" {
			t.Fatalf("mailto = %q", query.Get("mailto"))
		}
		if !strings.Contains(query.Get("query.bibliographic"), "A Paper") {
			t.Fatalf("query.bibliographic = %q", query.Get("query.bibliographic"))
		}
		if strings.Contains(query.Get("query.bibliographic"), "Ada Lovelace") {
			t.Fatalf("author unexpectedly included in Crossref query: %q", query.Get("query.bibliographic"))
		}
		_, _ = io.WriteString(writer, `{"message":{"items":[{
			"DOI":"10.1000/crossref",
			"URL":"https://doi.org/10.1000/crossref",
			"title":["A Paper"],
			"abstract":"<jats:p>An abstract</jats:p>",
			"author":[{
				"given":"Ada",
				"family":"Lovelace",
				"affiliation":[{"name":"Analytical Engine Institute"}]
			}],
			"subject":["Computing"],
			"publisher":"Example Publisher",
			"container-title":["Example Journal"],
			"published-print":{"date-parts":[[2024,2,3]]},
			"volume":"2",
			"issue":"1",
			"page":"1-4",
			"language":"EN"
		}]}}`)
	}))
	defer server.Close()

	provider, err := newCrossrefProvider(
		server.URL,
		server.Client(),
		"ops@example.test",
		"catalog-test/1.0",
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), types.PaperLookupQuery{
		Title:   "A Paper",
		Authors: []string{"Ada Lovelace"},
		Limit:   4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Abstract != "An abstract" ||
		candidate.Venue != "Example Journal" ||
		candidate.PublishedAt == nil ||
		candidate.PublishedAt.Format(time.DateOnly) != "2024-02-03" {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
}

// TestArXivSearchNormalizesAtomResponse verifies that a title search parses the
// Atom feed and normalizes the versionless ID, author affiliation, and journal DOI.
func TestArXivSearchNormalizesAtomResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/api/query" || query.Get("max_results") != "2" {
			t.Fatalf("unexpected request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		if got := query.Get("search_query"); got != `ti:"A Paper"` {
			t.Fatalf("search_query = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/atom+xml" {
			t.Fatalf("accept = %q", got)
		}
		writer.Header().Set("Content-Type", "application/atom+xml")
		_, _ = io.WriteString(writer, `<?xml version="1.0" encoding="UTF-8"?>
		<feed xmlns="http://www.w3.org/2005/Atom"
		      xmlns:arxiv="http://arxiv.org/schemas/atom">
		  <entry>
		    <id>http://arxiv.org/abs/2401.01234v2</id>
		    <published>2024-01-03T12:00:00Z</published>
		    <title>A
		      Paper</title>
		    <summary>An abstract.</summary>
		    <author><name>Ada Lovelace</name><arxiv:affiliation>Institute</arxiv:affiliation></author>
		    <arxiv:doi>10.1000/arxiv</arxiv:doi>
		    <arxiv:journal_ref>Example Journal 2 (2024)</arxiv:journal_ref>
		    <category term="cs.DL"/>
		    <link href="https://arxiv.org/abs/2401.01234" rel="alternate"/>
		  </entry>
		</feed>`)
	}))
	defer server.Close()

	provider, err := newArXivProvider(
		server.URL,
		server.Client(),
		"catalog-test/1.0",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), types.PaperLookupQuery{
		Title:   "A Paper",
		Authors: []string{"Ada Lovelace"},
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ProviderID != "2401.01234" ||
		candidate.Title != "A Paper" ||
		candidate.Authors[0].Affiliation != "Institute" ||
		candidate.DOI != "10.1000/arxiv" {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
}

// TestArXivSearchUsesExactIDForNativeURL verifies that an arxiv.org PDF URL is
// resolved through the exact id_list query rather than a title search.
func TestArXivSearchUsesExactIDForNativeURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if got := query.Get("id_list"); got != "2401.01234v2" {
			t.Fatalf("id_list = %q", got)
		}
		if query.Has("search_query") {
			t.Fatalf("exact lookup unexpectedly used search_query: %s", request.URL.RawQuery)
		}
		_, _ = io.WriteString(writer, `<?xml version="1.0"?>
		<feed xmlns="http://www.w3.org/2005/Atom">
		  <entry>
		    <id>https://arxiv.org/abs/2401.01234v2</id>
		    <title>Exact arXiv Paper</title>
		    <author><name>Ada Lovelace</name></author>
		  </entry>
		</feed>`)
	}))
	defer server.Close()

	provider, err := newArXivProvider(server.URL, server.Client(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := provider.Search(context.Background(), types.PaperLookupQuery{
		SourceURL: "https://arxiv.org/pdf/2401.01234v2.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ProviderID != "2401.01234" {
		t.Fatalf("unexpected exact candidates: %#v", candidates)
	}
}

// TestArXivCandidateDerivesDataCiteDOIWhenEntryHasNoDOI verifies that an arXiv
// entry without a publisher DOI receives its deterministic DataCite DOI.
func TestArXivCandidateDerivesDataCiteDOIWhenEntryHasNoDOI(t *testing.T) {
	candidate := (arXivEntry{
		ID:    "https://arxiv.org/abs/2512.19918v2",
		Title: "Widget2Code",
	}).candidate(1, time.Now())

	if candidate.ProviderID != "2512.19918" {
		t.Fatalf("provider id = %q", candidate.ProviderID)
	}
	if candidate.DOI != "10.48550/arXiv.2512.19918" {
		t.Fatalf("derived DOI = %q", candidate.DOI)
	}
}

// TestArXivCandidateKeepsExplicitJournalDOI verifies that an entry's explicit
// journal DOI takes precedence over the derived DataCite DOI.
func TestArXivCandidateKeepsExplicitJournalDOI(t *testing.T) {
	candidate := (arXivEntry{
		ID:    "https://arxiv.org/abs/2512.19918v2",
		DOI:   "10.1145/example",
		Title: "Widget2Code",
	}).candidate(1, time.Now())

	if candidate.DOI != "10.1145/example" {
		t.Fatalf("explicit DOI = %q", candidate.DOI)
	}
}

// TestParseArXivIdentifier verifies extraction of arXiv IDs from abs, PDF,
// arXiv:, and DataCite DOI forms while rejecting unrelated URLs and strings.
func TestParseArXivIdentifier(t *testing.T) {
	tests := map[string]string{
		"https://arxiv.org/abs/2401.01234v2":              "2401.01234v2",
		"https://export.arxiv.org/pdf/hep-th/9901001.pdf": "hep-th/9901001",
		"arXiv:2401.01234":                                "2401.01234",
		"10.48550/arXiv.2401.01234":                       "2401.01234",
		"https://doi.org/10.48550/arXiv.2401.01234":       "2401.01234",
		"https://example.test/abs/2401.01234":             "",
		"not-an-arxiv-id":                                 "",
	}
	for input, want := range tests {
		if got := parseArXivIdentifier(input); got != want {
			t.Errorf("parseArXivIdentifier(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestNonArXivProviderRejectsURLOnlyQuery verifies that providers lacking arXiv
// ID support reject source-URL-only queries as invalid data.
func TestNonArXivProviderRejectsURLOnlyQuery(t *testing.T) {
	provider, err := newOpenAlexProvider(
		"https://example.test",
		http.DefaultClient,
		"",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), types.PaperLookupQuery{
		SourceURL: "https://arxiv.org/abs/2401.01234",
	})
	if !errors.Is(err, types.ErrInvalidPaperCatalogData) {
		t.Fatalf("expected provider query validation error, got %v", err)
	}
}

// TestHTTPAdapterClassifiesRateLimit verifies that a 429 response becomes a
// retryable ProviderError with Retry-After seconds and without leaking body text.
func TestHTTPAdapterClassifiesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "7")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, "sensitive provider body")
	}))
	defer server.Close()

	adapter, err := newHTTPAdapter("test", server.URL, server.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.get(context.Background(), "/", nil, nil)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if !providerErr.Retryable ||
		providerErr.StatusCode != http.StatusTooManyRequests ||
		providerErr.RetryAfter != 7*time.Second {
		t.Fatalf("unexpected provider error: %#v", providerErr)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("provider response body leaked: %v", err)
	}
}

// TestHTTPAdapterRejectsOversizedResponse verifies that responses beyond the
// catalog body limit fail as a non-retryable ProviderError.
func TestHTTPAdapterRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", maxCatalogBodyBytes+1))
	}))
	defer server.Close()

	adapter, err := newHTTPAdapter("test", server.URL, server.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.get(context.Background(), "/", nil, nil)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Retryable {
		t.Fatalf("expected non-retryable ProviderError, got %T: %v", err, err)
	}
}

// TestRequestGateHonorsCancellation verifies that a throttled request gate
// returns context cancellation instead of waiting for the next allowed slot.
func TestRequestGateHonorsCancellation(t *testing.T) {
	gate := &requestGate{interval: time.Minute}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from providers_live_test.go.
// ---------------------------------------------------------------------------

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
