package doicitation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"paperMaster/internal/types"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from bibtex_test.go.
// ---------------------------------------------------------------------------

// TestInspectSingleBibTeXEntry verifies that one well-formed entry yields its
// citation key while HTML, comments, multiple entries, and truncated input are rejected.
func TestInspectSingleBibTeXEntry(t *testing.T) {
	key, err := inspectSingleBibTeXEntry(`@article{provider-key,
  title = {A title with {nested} braces},
  note = "quoted } text"
}`)
	if err != nil {
		t.Fatal(err)
	}
	if key != "provider-key" {
		t.Fatalf("key = %q", key)
	}
	for _, invalid := range []string{
		"", "<html>no</html>", "@comment{x}",
		"@article{one,title={A}}\n@article{two,title={B}}",
		"@article{missing,title={A}",
	} {
		if _, err := inspectSingleBibTeXEntry(invalid); err == nil {
			t.Fatalf("inspectSingleBibTeXEntry(%q) unexpectedly succeeded", invalid)
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated from client_test.go.
// ---------------------------------------------------------------------------

// TestClientFetchesBibTeXAndCachesSuccess verifies that DOI casing and URL
// prefixes normalize to one request and that the parsed citation is cached.
func TestClientFetchesBibTeXAndCachesSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if got := request.Header.Get("Accept"); got != "application/x-bibtex" {
			t.Errorf("Accept = %q", got)
		}
		writer.Header().Set("Content-Type", "application/x-bibtex")
		_, _ = writer.Write([]byte("  @article{remote-key, title={Remote}}\n"))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := newClient(baseURL, server.Client(), time.Second, 4096, time.Minute, 10, "test-agent")
	first, err := client.FetchBibTeX(context.Background(), "https://doi.org/10.1000/Cache-Test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.FetchBibTeX(context.Background(), "10.1000/cache-test")
	if err != nil {
		t.Fatal(err)
	}
	if first.BibTeX != "@article{remote-key, title={Remote}}" || first.CitationKey != "remote-key" || second != first {
		t.Fatalf("unexpected citations: %#v %#v", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

// TestClientExpiresSuccessfulCacheEntry verifies that a cached citation is
// fetched again after its TTL elapses.
func TestClientExpiresSuccessfulCacheEntry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = writer.Write([]byte("@misc{expiring, title={Expiring}}"))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := newClient(baseURL, server.Client(), time.Second, 4096, time.Millisecond, 10, "test-agent")
	if _, err := client.FetchBibTeX(context.Background(), "10.1000/expiring"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := client.FetchBibTeX(context.Background(), "10.1000/expiring"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after TTL expiry", calls.Load())
	}
}

// TestClientEncodesDOIPathSegments verifies that DOI path characters such as
// parentheses are percent-encoded when building the upstream request.
func TestClientEncodesDOIPathSegments(t *testing.T) {
	var escapedPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		escapedPath = request.URL.EscapedPath()
		_, _ = writer.Write([]byte("@misc{encoded, title={Encoded}}"))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := newClient(baseURL, server.Client(), time.Second, 4096, time.Minute, 10, "test-agent")
	if _, err := client.FetchBibTeX(context.Background(), "10.1000/name(1)"); err != nil {
		t.Fatal(err)
	}
	if escapedPath != "/10.1000/name%281%29" {
		t.Fatalf("escaped path = %q", escapedPath)
	}
}

// TestClientRedirectPolicyAllowsNegotiationHostsOnly verifies that redirects
// stay on the DOI negotiation hosts and HTTPS, rejecting other hosts or HTTP.
func TestClientRedirectPolicyAllowsNegotiationHostsOnly(t *testing.T) {
	baseURL, _ := url.Parse("https://doi.org")
	client := newClient(baseURL, nil, time.Second, 4096, time.Minute, 10, "test-agent")
	for _, target := range []string{
		"https://api.crossref.org/v1/works/10.1000%2Ftest/transform",
		"https://data.crosscite.org/10.1000%2Ftest",
	} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := client.httpClient.CheckRedirect(request, []*http.Request{{}}); err != nil {
			t.Fatalf("safe redirect %q rejected: %v", target, err)
		}
	}
	for _, target := range []string{"http://api.crossref.org/path", "https://publisher.example/path"} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if err := client.httpClient.CheckRedirect(request, []*http.Request{{}}); !errors.Is(err, errUnsafeRedirect) {
			t.Fatalf("unsafe redirect %q error = %v", target, err)
		}
	}
}

// TestClientRejectsHTMLAndDoesNotCacheFailure verifies that an HTML response
// is reported as an error and that failed lookups are retried rather than cached.
func TestClientRejectsHTMLAndDoesNotCacheFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("<html>publisher</html>"))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := newClient(baseURL, server.Client(), time.Second, 4096, time.Minute, 10, "test-agent")
	for range 2 {
		if _, err := client.FetchBibTeX(context.Background(), "10.1000/failure"); err == nil {
			t.Fatal("expected HTML response failure")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

// TestClientCoalescesInflightRequests verifies that concurrent fetches for the
// same DOI share one upstream request and both receive the result.
func TestClientCoalescesInflightRequests(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-release
		_, _ = writer.Write([]byte("@misc{shared, title={Shared}}"))
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := newClient(baseURL, server.Client(), time.Second, 4096, time.Minute, 10, "test-agent")
	errors := make(chan error, 2)
	for range 2 {
		go func() { _, err := client.FetchBibTeX(context.Background(), "10.1000/shared"); errors <- err }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

// TestClientClassifiesBoundedResponseFailures verifies the stable error codes
// returned for status, empty/HTML body, size-limit, and malformed BibTeX failures.
func TestClientClassifiesBoundedResponseFailures(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		maxBytes    int64
		wantCode    string
	}{
		{name: "not found", status: http.StatusNotFound, wantCode: "http_404"},
		{name: "not acceptable", status: http.StatusNotAcceptable, wantCode: "http_406"},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: "http_429"},
		{name: "server error", status: http.StatusBadGateway, wantCode: "http_502"},
		{name: "empty", status: http.StatusOK, wantCode: "empty_response"},
		{name: "html", status: http.StatusOK, contentType: "text/html", body: "<html>no</html>", wantCode: "html_response"},
		{name: "oversized", status: http.StatusOK, body: "@misc{large,title={Large}}", maxBytes: 8, wantCode: "body_too_large"},
		{name: "malformed", status: http.StatusOK, body: "not bibtex", wantCode: "invalid_bibtex"},
		{name: "multiple", status: http.StatusOK, body: "@misc{one,title={One}}\n@misc{two,title={Two}}", wantCode: "invalid_bibtex"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if test.contentType != "" {
					writer.Header().Set("Content-Type", test.contentType)
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			baseURL, _ := url.Parse(server.URL)
			maxBytes := test.maxBytes
			if maxBytes == 0 {
				maxBytes = 4096
			}
			client := newClient(baseURL, server.Client(), time.Second, maxBytes, time.Minute, 10, "test-agent")
			_, err := client.FetchBibTeX(context.Background(), "10.1000/failure")
			var citationErr *types.DOICitationError
			if !errors.As(err, &citationErr) || citationErr.Code != test.wantCode {
				t.Fatalf("error = %#v, want code %q", err, test.wantCode)
			}
		})
	}
}

// TestClientTimeoutAndCancellation verifies that upstream timeouts and caller
// cancellation are surfaced as distinct DOICitationError codes.
func TestClientTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	baseURL, _ := url.Parse(server.URL)
	client := newClient(baseURL, server.Client(), 20*time.Millisecond, 4096, time.Minute, 10, "test-agent")
	_, err := client.FetchBibTeX(context.Background(), "10.1000/timeout")
	var citationErr *types.DOICitationError
	if !errors.As(err, &citationErr) || citationErr.Code != "timeout" {
		t.Fatalf("timeout error = %#v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.FetchBibTeX(ctx, "10.1000/cancelled")
	if !errors.As(err, &citationErr) || citationErr.Code != "cancelled" {
		t.Fatalf("cancellation error = %#v", err)
	}
}
