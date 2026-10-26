package paperurl

import (
	"context"
	"errors"
	"paperMaster/internal/limit"
	"paperMaster/internal/types"
	"paperMaster/internal/utils"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from limiter_test.go.
// ---------------------------------------------------------------------------

func newMemoryDownloadLimiter(global, perHost int, spacing time.Duration) *downloadLimiter {
	return &downloadLimiter{
		global:  global,
		max:     perHost,
		spacing: spacing,
		limiter: limit.NewMemoryEngine(),
	}
}

// TestDownloadLimiterEnforcesGlobalConcurrencyAcrossHosts verifies that the
// global cap blocks a second host until the first lease is released.
func TestDownloadLimiterEnforcesGlobalConcurrencyAcrossHosts(t *testing.T) {
	limiter := newMemoryDownloadLimiter(1, 1, 0)
	release, err := limiter.Acquire(context.Background(), "one.example")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx, "two.example"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v, want global concurrency timeout", err)
	}
	release()
	secondRelease, err := limiter.Acquire(context.Background(), "two.example")
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	secondRelease()
}

// TestDownloadLimiterHonorsHostCooldown verifies that acquiring a lease waits
// until the host's cooldown has elapsed.
func TestDownloadLimiterHonorsHostCooldown(t *testing.T) {
	limiter := newMemoryDownloadLimiter(2, 1, 0)
	if err := limiter.Cooldown(context.Background(), "paper.example", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	release, err := limiter.Acquire(context.Background(), "paper.example")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("Acquire() waited %s, want cooldown wait", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from resolver_test.go.
// ---------------------------------------------------------------------------

// TestResolverCanonicalizesArXivLandingAndPDFURLs verifies that an export arXiv
// abs URL is rewritten to the canonical landing page and PDF download URL.
func TestResolverCanonicalizesArXivLandingAndPDFURLs(t *testing.T) {
	resolved, err := NewResolver().Resolve(context.Background(), types.PaperURLResolveRequest{
		SourceURL: "https://export.arxiv.org/abs/2401.01234v2",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.LandingURL != "https://arxiv.org/abs/2401.01234v2" || resolved.DownloadURL != "https://arxiv.org/pdf/2401.01234v2.pdf" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

// TestResolverDoesNotGuessPublisherPDF verifies that a generic publisher page is
// not treated as a PDF even with an explicit DOI, which is still normalized.
func TestResolverDoesNotGuessPublisherPDF(t *testing.T) {
	allowResolverHost(t, "publisher.example")
	_, err := NewResolver().Resolve(context.Background(), types.PaperURLResolveRequest{
		SourceURL:   "https://publisher.example/articles/10.1000/source",
		ExplicitDOI: "https://doi.org/10.5555/EXPLICIT",
	})
	if !errors.Is(err, types.ErrRemotePDFUnavailable) {
		t.Fatalf("Resolve() error = %v, want ErrRemotePDFUnavailable", err)
	}
	if got := types.NormalizeDOI("https://doi.org/10.5555/EXPLICIT"); got != "10.5555/explicit" {
		t.Fatalf("NormalizeDOI() = %q", got)
	}
}

// TestResolverClassifiesSSRFRejectionAsInvalidRequest verifies that a loopback
// source URL is rejected as an invalid remote-PDF request.
func TestResolverClassifiesSSRFRejectionAsInvalidRequest(t *testing.T) {
	_, err := NewResolver().Resolve(context.Background(), types.PaperURLResolveRequest{
		SourceURL: "http://127.0.0.1/private.pdf",
	})
	if !errors.Is(err, types.ErrInvalidRemotePDFRequest) {
		t.Fatalf("Resolve() error = %v, want ErrInvalidRemotePDFRequest", err)
	}
}

// TestResolverAcceptsExplicitGenericPDF verifies that a direct .PDF URL on an
// allowed host is accepted and its explicit DOI is normalized.
func TestResolverAcceptsExplicitGenericPDF(t *testing.T) {
	allowResolverHost(t, "files.example")
	resolved, err := NewResolver().Resolve(context.Background(), types.PaperURLResolveRequest{
		SourceURL:   "https://files.example/papers/example.PDF?download=1",
		ExplicitDOI: "doi:10.1000/Example",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.DownloadURL == "" || resolved.DOI != "10.1000/example" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func allowResolverHost(t *testing.T, host string) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", host)
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}

// TestResolverDerivesArXivURLFromKnownDOIOnly verifies that an arXiv DataCite
// DOI alone produces a deterministic PDF download URL.
func TestResolverDerivesArXivURLFromKnownDOIOnly(t *testing.T) {
	resolved, err := NewResolver().Resolve(context.Background(), types.PaperURLResolveRequest{
		ExplicitDOI: "10.48550/arXiv.2401.01234",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.DownloadURL != "https://arxiv.org/pdf/2401.01234.pdf" {
		t.Fatalf("download URL = %q", resolved.DownloadURL)
	}
}

// TestResolverDerivesArXivURLFromDOILandingURL verifies that a doi.org landing
// URL for an arXiv DOI is mapped to canonical abs/PDF URLs and a normalized DOI.
func TestResolverDerivesArXivURLFromDOILandingURL(t *testing.T) {
	resolved, err := NewResolver().Resolve(context.Background(), types.PaperURLResolveRequest{
		SourceURL: "https://doi.org/10.48550/arXiv.2401.01234",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.LandingURL != "https://arxiv.org/abs/2401.01234" ||
		resolved.DownloadURL != "https://arxiv.org/pdf/2401.01234.pdf" ||
		resolved.DOI != "10.48550/arxiv.2401.01234" {
		t.Fatalf("resolved = %#v", resolved)
	}
}
