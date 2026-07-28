package papercatalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"
)

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

func TestArXivSearchNormalizesAtomResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/api/query" || query.Get("max_results") != "2" {
			t.Fatalf("unexpected request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		if got := query.Get("search_query"); got != `ti:"A Paper" AND au:"Ada Lovelace"` {
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
