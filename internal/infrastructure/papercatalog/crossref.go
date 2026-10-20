package papercatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

const (
	// CrossrefProvider is retained as a package compatibility alias. New code
	// should use types.PaperCatalogProviderCrossref.
	CrossrefProvider = string(types.PaperCatalogProviderCrossref)
	crossrefBaseURL  = "https://api.crossref.org"
)

type crossrefProvider struct {
	http   httpAdapter
	mailto string
}

// NewCrossrefProvider creates the production Crossref adapter. A contact
// email opts requests into Crossref's polite pool when configured.
func NewCrossrefProvider(
	client *http.Client,
	mailto string,
	userAgent string,
) (interfaces.PaperCatalogProvider, error) {
	return newCrossrefProvider(
		crossrefBaseURL,
		client,
		mailto,
		userAgent,
	)
}

func newCrossrefProvider(
	baseURL string,
	client *http.Client,
	mailto string,
	userAgent string,
) (*crossrefProvider, error) {
	adapter, err := newHTTPAdapter(
		CrossrefProvider,
		baseURL,
		client,
		userAgent,
	)
	if err != nil {
		return nil, err
	}
	return &crossrefProvider{
		http:   adapter,
		mailto: strings.TrimSpace(mailto),
	}, nil
}

func (provider *crossrefProvider) Name() string {
	return CrossrefProvider
}

func (provider *crossrefProvider) Search(
	ctx context.Context,
	query types.PaperLookupQuery,
) ([]types.PaperCatalogCandidate, error) {
	query, err := validateLookupQuery(query)
	if err != nil {
		return nil, err
	}
	if err := requireDOIOrTitle(query); err != nil {
		return nil, err
	}
	values := url.Values{
		"rows": {strconv.Itoa(query.EffectiveLimit())},
	}
	if provider.mailto != "" {
		values.Set("mailto", provider.mailto)
	}
	if query.DOI != "" {
		values.Set("filter", "doi:"+query.DOI)
	} else {
		parts := []string{query.Title}
		if query.PublishedYear > 0 {
			parts = append(parts, strconv.Itoa(query.PublishedYear))
		}
		values.Set("query.bibliographic", strings.Join(parts, " "))
	}
	body, err := provider.http.get(ctx, "/works", values, nil)
	if err != nil {
		provider.logFailure(ctx, err)
		return nil, err
	}
	var response crossrefResponse
	if err := json.Unmarshal(body, &response); err != nil {
		err = fmt.Errorf("decode Crossref response: %w", err)
		provider.logFailure(ctx, err)
		return nil, err
	}
	retrievedAt := time.Now().UTC()
	candidates := make([]types.PaperCatalogCandidate, 0, len(response.Message.Items))
	for index, work := range response.Message.Items {
		if firstString(work.Title) == "" ||
			(strings.TrimSpace(work.DOI) == "" && strings.TrimSpace(work.URL) == "") {
			continue
		}
		candidates = append(candidates, work.candidate(
			rankScore(index, len(response.Message.Items)),
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

func (provider *crossrefProvider) logFailure(ctx context.Context, err error) {
	logger.Error(ctx, "paper catalog search failed",
		"provider", provider.Name(),
		"error", err)
}

type crossrefResponse struct {
	Message struct {
		Items []crossrefWork `json:"items"`
	} `json:"message"`
}

type crossrefWork struct {
	DOI             string           `json:"DOI"`
	URL             string           `json:"URL"`
	Title           []string         `json:"title"`
	Abstract        string           `json:"abstract"`
	Author          []crossrefAuthor `json:"author"`
	Subject         []string         `json:"subject"`
	Publisher       string           `json:"publisher"`
	ContainerTitle  []string         `json:"container-title"`
	Published       crossrefDate     `json:"published"`
	PublishedPrint  crossrefDate     `json:"published-print"`
	PublishedOnline crossrefDate     `json:"published-online"`
	Volume          string           `json:"volume"`
	Issue           string           `json:"issue"`
	Page            string           `json:"page"`
	Language        string           `json:"language"`
}

type crossrefAuthor struct {
	Given       string `json:"given"`
	Family      string `json:"family"`
	Affiliation []struct {
		Name string `json:"name"`
	} `json:"affiliation"`
}

type crossrefDate struct {
	DateParts [][]int `json:"date-parts"`
}

func (work crossrefWork) candidate(
	score float64,
	retrievedAt time.Time,
) types.PaperCatalogCandidate {
	authors := make([]types.PaperAuthor, 0, len(work.Author))
	for _, author := range work.Author {
		affiliation := ""
		if len(author.Affiliation) > 0 {
			affiliation = author.Affiliation[0].Name
		}
		authors = append(authors, types.PaperAuthor{
			Name:        strings.TrimSpace(author.Given + " " + author.Family),
			Affiliation: affiliation,
		})
	}
	publishedAt := crossrefPublishedAt(
		work.PublishedPrint,
		work.PublishedOnline,
		work.Published,
	)
	return types.PaperCatalogCandidate{
		Provider:      CrossrefProvider,
		ProviderID:    firstNonEmpty(work.DOI, work.URL),
		DOI:           work.DOI,
		Title:         firstString(work.Title),
		Abstract:      work.Abstract,
		Authors:       authors,
		Tags:          work.Subject,
		SourceName:    work.Publisher,
		SourceURL:     work.URL,
		Venue:         firstString(work.ContainerTitle),
		PublishedAt:   publishedAt,
		Volume:        work.Volume,
		Issue:         work.Issue,
		Pages:         work.Page,
		Language:      work.Language,
		ProviderScore: score,
		RetrievedAt:   retrievedAt,
	}
}

func crossrefPublishedAt(values ...crossrefDate) *time.Time {
	for _, value := range values {
		if len(value.DateParts) == 0 || len(value.DateParts[0]) == 0 {
			continue
		}
		parts := value.DateParts[0]
		year, month, day := parts[0], 1, 1
		if len(parts) > 1 && parts[1] >= 1 && parts[1] <= 12 {
			month = parts[1]
		}
		if len(parts) > 2 && parts[2] >= 1 && parts[2] <= 31 {
			day = parts[2]
		}
		result := time.Date(
			year,
			time.Month(month),
			day,
			0,
			0,
			0,
			0,
			time.UTC,
		)
		return &result
	}
	return nil
}

func firstString(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var _ interfaces.PaperCatalogProvider = (*crossrefProvider)(nil)
