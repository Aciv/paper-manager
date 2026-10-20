package papercatalog

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

const (
	// ArXivProvider is retained as a package compatibility alias. New code
	// should use types.PaperCatalogProviderArXiv, the central provider identity.
	ArXivProvider       = string(types.PaperCatalogProviderArXiv)
	arXivBaseURL        = "https://export.arxiv.org"
	arXivRequestSpacing = 3 * time.Second
	arXivDOIPrefix      = "10.48550/arXiv."
)

var arXivVersionPattern = regexp.MustCompile(`v[0-9]+$`)
var arXivIdentifierPattern = regexp.MustCompile(
	`(?i)^(?:[0-9]{4}\.[0-9]{4,5}|[a-z][a-z0-9.-]*/[0-9]{7})(?:v[0-9]+)?$`,
)

type arXivProvider struct {
	http httpAdapter
	gate *requestGate
}

// NewArXivProvider creates the production arXiv adapter. Requests are spaced
// by three seconds in accordance with arXiv's API usage guidance.
func NewArXivProvider(
	client *http.Client,
	userAgent string,
	requestSpacing ...time.Duration,
) (interfaces.PaperCatalogProvider, error) {
	spacing := arXivRequestSpacing
	if len(requestSpacing) > 0 {
		spacing = requestSpacing[0]
	}
	return newArXivProvider(
		arXivBaseURL,
		client,
		userAgent,
		spacing,
	)
}

func newArXivProvider(
	baseURL string,
	client *http.Client,
	userAgent string,
	requestSpacing time.Duration,
) (*arXivProvider, error) {
	adapter, err := newHTTPAdapter(
		ArXivProvider,
		baseURL,
		client,
		userAgent,
	)
	if err != nil {
		return nil, err
	}
	return &arXivProvider{
		http: adapter,
		gate: &requestGate{interval: requestSpacing},
	}, nil
}

func (provider *arXivProvider) Name() string {
	return ArXivProvider
}

func (provider *arXivProvider) Search(
	ctx context.Context,
	query types.PaperLookupQuery,
) ([]types.PaperCatalogCandidate, error) {
	query, err := validateLookupQuery(query)
	if err != nil {
		return nil, err
	}
	if err := provider.gate.Wait(ctx); err != nil {
		return nil, err
	}

	values := url.Values{
		"start":       {"0"},
		"max_results": {strconv.Itoa(query.EffectiveLimit())},
		"sortBy":      {"relevance"},
		"sortOrder":   {"descending"},
	}
	if identifier, ok := arXivIdentifierFromQuery(query); ok {
		values.Set("id_list", identifier)
	} else if query.DOI != "" {
		values.Set("search_query", `doi:"`+arXivQueryValue(query.DOI)+`"`)
	} else {
		// Retrieval is title-first. Parser author output can be incomplete or
		// collapse a whole byline into one value; author evidence is therefore
		// applied only when scoring a weak title candidate after retrieval.
		values.Set("search_query", `ti:"`+arXivQueryValue(query.Title)+`"`)
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/atom+xml")
	body, err := provider.http.get(ctx, "/api/query", values, headers)
	if err != nil {
		provider.logFailure(ctx, err)
		return nil, err
	}

	var feed arXivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		err = fmt.Errorf("decode arXiv response: %w", err)
		provider.logFailure(ctx, err)
		return nil, err
	}
	retrievedAt := time.Now().UTC()
	candidates := make([]types.PaperCatalogCandidate, 0, len(feed.Entries))
	for index, entry := range feed.Entries {
		if strings.TrimSpace(entry.ID) == "" ||
			strings.TrimSpace(entry.Title) == "" {
			continue
		}
		candidates = append(candidates, entry.candidate(
			rankScore(index, len(feed.Entries)),
			retrievedAt,
		))
	}
	candidates, err = normalizeCandidates(candidates, query.EffectiveLimit())
	if err != nil {
		provider.logFailure(ctx, err)
		return nil, err
	}
	logger.Info(ctx, "paper catalog search completed",
		"provider", provider.Name(),
		"candidate_count", len(candidates))
	return candidates, nil
}

func (provider *arXivProvider) logFailure(ctx context.Context, err error) {
	logger.Error(ctx, "paper catalog search failed",
		"provider", provider.Name(),
		"error", err)
}

type arXivFeed struct {
	Entries []arXivEntry `xml:"entry"`
}

type arXivEntry struct {
	ID         string        `xml:"id"`
	Published  string        `xml:"published"`
	Title      string        `xml:"title"`
	Summary    string        `xml:"summary"`
	DOI        string        `xml:"doi"`
	JournalRef string        `xml:"journal_ref"`
	Authors    []arXivAuthor `xml:"author"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
	Links []struct {
		Href  string `xml:"href,attr"`
		Rel   string `xml:"rel,attr"`
		Title string `xml:"title,attr"`
	} `xml:"link"`
}

type arXivAuthor struct {
	Name        string `xml:"name"`
	Affiliation string `xml:"affiliation"`
}

func (entry arXivEntry) candidate(
	score float64,
	retrievedAt time.Time,
) types.PaperCatalogCandidate {
	authors := make([]types.PaperAuthor, 0, len(entry.Authors))
	for _, author := range entry.Authors {
		authors = append(authors, types.PaperAuthor{
			Name:        author.Name,
			Affiliation: author.Affiliation,
		})
	}
	tags := make([]string, 0, len(entry.Categories))
	for _, category := range entry.Categories {
		tags = append(tags, category.Term)
	}
	sourceURL := strings.TrimSpace(entry.ID)
	for _, link := range entry.Links {
		if link.Rel == "alternate" && strings.TrimSpace(link.Href) != "" {
			sourceURL = link.Href
			break
		}
	}
	providerID := arXivID(entry.ID)
	doi := strings.TrimSpace(entry.DOI)
	if doi == "" {
		doi = arXivDOI(providerID)
	}
	return types.PaperCatalogCandidate{
		Provider:      ArXivProvider,
		ProviderID:    providerID,
		DOI:           doi,
		Title:         entry.Title,
		Abstract:      entry.Summary,
		Authors:       authors,
		Tags:          tags,
		SourceName:    "arXiv",
		SourceURL:     sourceURL,
		Venue:         entry.JournalRef,
		PublishedAt:   parseDate(entry.Published),
		ProviderScore: score,
		RetrievedAt:   retrievedAt,
	}
}

func arXivDOI(identifier string) string {
	identifier = arXivID(identifier)
	if identifier == "" {
		return ""
	}
	return arXivDOIPrefix + identifier
}

func arXivID(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		value = value[index+1:]
	}
	return arXivVersionPattern.ReplaceAllString(value, "")
}

func arXivIdentifierFromQuery(query types.PaperLookupQuery) (string, bool) {
	for _, value := range []string{query.SourceURL, query.DOI} {
		if identifier := parseArXivIdentifier(value); identifier != "" {
			return identifier, true
		}
	}
	return "", false
}

func parseArXivIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
		isArXivHost := host == "arxiv.org" || strings.HasSuffix(host, ".arxiv.org")
		isDOIHost := host == "doi.org" || host == "dx.doi.org"
		if !isArXivHost && !isDOIHost {
			return ""
		}
		path := strings.Trim(parsed.EscapedPath(), "/")
		unescaped, err := url.PathUnescape(path)
		if err != nil {
			return ""
		}
		value = unescaped
		if isArXivHost {
			for _, prefix := range []string{"abs/", "pdf/", "html/"} {
				if strings.HasPrefix(strings.ToLower(value), prefix) {
					value = value[len(prefix):]
					break
				}
			}
			if strings.HasSuffix(strings.ToLower(value), ".pdf") {
				value = value[:len(value)-len(".pdf")]
			}
		}
	}
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "doi:"):
		value = strings.TrimSpace(value[len("doi:"):])
		lower = strings.ToLower(value)
	case strings.HasPrefix(lower, "arxiv:"):
		value = strings.TrimSpace(value[len("arxiv:"):])
		lower = strings.ToLower(value)
	}
	const arXivDOIPrefix = "10.48550/arxiv."
	if strings.HasPrefix(lower, arXivDOIPrefix) {
		value = value[len(arXivDOIPrefix):]
	}
	value = strings.TrimSpace(value)
	if !arXivIdentifierPattern.MatchString(value) {
		return ""
	}
	return value
}

func arXivQueryValue(value string) string {
	value = strings.ReplaceAll(value, `\`, " ")
	value = strings.ReplaceAll(value, `"`, " ")
	return normalizeSpace(value)
}

// requestGate serializes reservations for providers that require a minimum
// interval between calls. Waiting remains context-aware so shutdown and task
// cancellation do not stall behind the rate limiter.
type requestGate struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (gate *requestGate) Wait(ctx context.Context) error {
	if gate == nil || gate.interval <= 0 {
		return nil
	}
	gate.mu.Lock()
	now := time.Now()
	waitUntil := gate.next
	if waitUntil.Before(now) {
		waitUntil = now
	}
	gate.next = waitUntil.Add(gate.interval)
	gate.mu.Unlock()

	delay := time.Until(waitUntil)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ interfaces.PaperCatalogProvider = (*arXivProvider)(nil)
