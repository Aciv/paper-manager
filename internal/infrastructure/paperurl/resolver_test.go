package paperurl

import (
	"context"
	"errors"
	"testing"

	"paperMaster/internal/types"
	"paperMaster/internal/utils"
)

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
