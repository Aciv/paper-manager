// Package paperurl resolves scholarly landing pages into explicit PDF URLs.
package paperurl

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"
)

var arxivPathPattern = regexp.MustCompile(`(?i)^/(?:abs|pdf)/([a-z-]+(?:\.[a-z-]+)?/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(v[0-9]+)?(?:\.pdf)?$`)

type resolver struct{}

func NewResolver() interfaces.PaperURLResolver { return &resolver{} }

func (r *resolver) Resolve(ctx context.Context, request types.PaperURLResolveRequest) (types.ResolvedPaperURL, error) {
	if err := ctx.Err(); err != nil {
		return types.ResolvedPaperURL{}, err
	}
	doi := types.NormalizeDOI(request.ExplicitDOI)
	if strings.HasPrefix(doi, "10.48550/arxiv.") {
		id := strings.TrimPrefix(doi, "10.48550/arxiv.")
		return types.ResolvedPaperURL{
			LandingURL:  "https://arxiv.org/abs/" + id,
			DownloadURL: "https://arxiv.org/pdf/" + id + ".pdf",
			DOI:         doi, Filename: idFilename(id),
		}, nil
	}

	raw := strings.TrimSpace(request.SourceURL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return types.ResolvedPaperURL{}, fmt.Errorf("%w: source URL must be absolute HTTP(S)", types.ErrRemotePDFUnavailable)
	}
	if err := utils.ValidateURLForSSRF(raw); err != nil {
		return types.ResolvedPaperURL{}, fmt.Errorf("validate source URL: %w", err)
	}
	if doi == "" {
		doi = types.ExtractDOIFromURL(raw)
	}
	// DOI URLs remain landing-page identifiers. The one currently supported
	// DOI-to-PDF mapping is arXiv's documented 10.48550 namespace.
	if strings.HasPrefix(doi, "10.48550/arxiv.") {
		id := strings.TrimPrefix(doi, "10.48550/arxiv.")
		return types.ResolvedPaperURL{
			LandingURL:  "https://arxiv.org/abs/" + id,
			DownloadURL: "https://arxiv.org/pdf/" + id + ".pdf",
			DOI:         doi, Filename: idFilename(id),
		}, nil
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "arxiv.org" || host == "www.arxiv.org" || host == "export.arxiv.org" {
		match := arxivPathPattern.FindStringSubmatch(parsed.Path)
		if len(match) == 0 {
			return types.ResolvedPaperURL{}, fmt.Errorf("%w: unsupported arXiv URL", types.ErrRemotePDFUnavailable)
		}
		id := match[1] + match[2]

		if !strings.HasPrefix(doi, "10.48550/arxiv.") {
			doi = "10.48550/arxiv." + id
		}

		landing := "https://arxiv.org/abs/" + id
		download := "https://arxiv.org/pdf/" + id + ".pdf"
		return types.ResolvedPaperURL{
			LandingURL:  landing,
			DownloadURL: download,
			DOI:         doi,
			Filename:    idFilename(id)}, nil
	}

	// A direct PDF URL is explicit user/provider evidence. Generic publisher
	// landing pages are intentionally not scraped or guessed here.
	if strings.EqualFold(path.Ext(parsed.Path), ".pdf") {
		return types.ResolvedPaperURL{
			LandingURL: raw, DownloadURL: raw, DOI: doi,
			Filename: types.SuggestedPDFFilename(raw),
		}, nil
	}
	return types.ResolvedPaperURL{}, fmt.Errorf("%w: generic landing page has no trusted PDF resolver", types.ErrRemotePDFUnavailable)
}

func idFilename(id string) string {
	return strings.NewReplacer("/", "-", "\\", "-").Replace(id) + ".pdf"
}

var _ interfaces.PaperURLResolver = (*resolver)(nil)
