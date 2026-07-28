package papercatalog

import (
	"context"
	"encoding/json"
	"errors"
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
	SemanticScholarProvider = "semantic_scholar"
	semanticScholarBaseURL  = "https://api.semanticscholar.org"
	semanticScholarFields   = "title,abstract,authors,externalIds,url,venue,year,publicationDate,journal,fieldsOfStudy"
)

type semanticScholarProvider struct {
	http   httpAdapter
	apiKey string
}

// NewSemanticScholarProvider creates the production Semantic Scholar adapter.
func NewSemanticScholarProvider(
	client *http.Client,
	apiKey string,
	userAgent string,
) (interfaces.PaperCatalogProvider, error) {
	return newSemanticScholarProvider(
		semanticScholarBaseURL,
		client,
		apiKey,
		userAgent,
	)
}

func newSemanticScholarProvider(
	baseURL string,
	client *http.Client,
	apiKey string,
	userAgent string,
) (*semanticScholarProvider, error) {
	adapter, err := newHTTPAdapter(
		SemanticScholarProvider,
		baseURL,
		client,
		userAgent,
	)
	if err != nil {
		return nil, err
	}
	return &semanticScholarProvider{
		http:   adapter,
		apiKey: strings.TrimSpace(apiKey),
	}, nil
}

func (provider *semanticScholarProvider) Name() string {
	return SemanticScholarProvider
}

func (provider *semanticScholarProvider) Search(
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
	if query.DOI != "" {
		candidates, found, lookupErr := provider.lookupDOI(ctx, query.DOI)
		if lookupErr != nil {
			provider.logFailure(ctx, lookupErr)
			return nil, lookupErr
		}
		if found || query.Title == "" {
			return candidates, nil
		}
	}

	values := url.Values{
		"query":  {strings.ReplaceAll(query.Title, "-", " ")},
		"limit":  {strconv.Itoa(query.EffectiveLimit())},
		"offset": {"0"},
		"fields": {semanticScholarFields},
	}
	if query.PublishedYear > 0 {
		values.Set("year", strconv.Itoa(query.PublishedYear))
	}
	body, err := provider.http.get(
		ctx,
		"/graph/v1/paper/search",
		values,
		provider.headers(),
	)
	if err != nil {
		provider.logFailure(ctx, err)
		return nil, err
	}
	var response semanticScholarSearchResponse
	if err := json.Unmarshal(body, &response); err != nil {
		err = fmt.Errorf("decode Semantic Scholar response: %w", err)
		provider.logFailure(ctx, err)
		return nil, err
	}
	candidates := make([]types.PaperCatalogCandidate, 0, len(response.Data))
	retrievedAt := time.Now().UTC()
	for index, paper := range response.Data {
		if strings.TrimSpace(paper.PaperID) == "" ||
			strings.TrimSpace(paper.Title) == "" {
			continue
		}
		candidates = append(candidates, paper.candidate(
			rankScore(index, len(response.Data)),
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

func (provider *semanticScholarProvider) lookupDOI(
	ctx context.Context,
	doi string,
) ([]types.PaperCatalogCandidate, bool, error) {
	body, err := provider.http.get(
		ctx,
		"/graph/v1/paper/DOI:"+doi,
		url.Values{"fields": {semanticScholarFields}},
		provider.headers(),
	)
	if err != nil {
		var providerErr *ProviderError
		if errors.As(err, &providerErr) &&
			providerErr.StatusCode == http.StatusNotFound {
			return []types.PaperCatalogCandidate{}, false, nil
		}
		return nil, false, err
	}
	var paper semanticScholarPaper
	if err := json.Unmarshal(body, &paper); err != nil {
		return nil, false, fmt.Errorf("decode Semantic Scholar DOI response: %w", err)
	}
	if strings.TrimSpace(paper.PaperID) == "" ||
		strings.TrimSpace(paper.Title) == "" {
		return []types.PaperCatalogCandidate{}, false, nil
	}
	candidates, err := normalizeCandidates(
		[]types.PaperCatalogCandidate{
			paper.candidate(1, time.Now().UTC()),
		},
		1,
	)
	return candidates, true, err
}

func (provider *semanticScholarProvider) headers() http.Header {
	headers := make(http.Header)
	if provider.apiKey != "" {
		headers.Set("x-api-key", provider.apiKey)
	}
	return headers
}

func (provider *semanticScholarProvider) logFailure(
	ctx context.Context,
	err error,
) {
	logger.Error(ctx, "paper catalog search failed",
		"provider", provider.Name(),
		"error", err)
}

type semanticScholarSearchResponse struct {
	Data []semanticScholarPaper `json:"data"`
}

type semanticScholarPaper struct {
	PaperID         string                     `json:"paperId"`
	ExternalIDs     map[string]json.RawMessage `json:"externalIds"`
	URL             string                     `json:"url"`
	Title           string                     `json:"title"`
	Abstract        string                     `json:"abstract"`
	Venue           string                     `json:"venue"`
	Year            int                        `json:"year"`
	PublicationDate string                     `json:"publicationDate"`
	Authors         []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Journal struct {
		Name   string `json:"name"`
		Volume string `json:"volume"`
		Pages  string `json:"pages"`
	} `json:"journal"`
	FieldsOfStudy []string `json:"fieldsOfStudy"`
}

func (paper semanticScholarPaper) candidate(
	score float64,
	retrievedAt time.Time,
) types.PaperCatalogCandidate {
	authors := make([]types.PaperAuthor, 0, len(paper.Authors))
	for _, author := range paper.Authors {
		authors = append(authors, types.PaperAuthor{Name: author.Name})
	}
	publishedAt := parseDate(paper.PublicationDate)
	if publishedAt == nil {
		publishedAt = yearDate(paper.Year)
	}
	venue := paper.Venue
	if strings.TrimSpace(paper.Journal.Name) != "" {
		venue = paper.Journal.Name
	}
	return types.PaperCatalogCandidate{
		Provider:      SemanticScholarProvider,
		ProviderID:    paper.PaperID,
		DOI:           semanticScholarExternalID(paper.ExternalIDs, "DOI"),
		Title:         paper.Title,
		Abstract:      paper.Abstract,
		Authors:       authors,
		Tags:          paper.FieldsOfStudy,
		SourceName:    "Semantic Scholar",
		SourceURL:     paper.URL,
		Venue:         venue,
		PublishedAt:   publishedAt,
		Volume:        paper.Journal.Volume,
		Pages:         paper.Journal.Pages,
		ProviderScore: score,
		RetrievedAt:   retrievedAt,
	}
}

func semanticScholarExternalID(
	externalIDs map[string]json.RawMessage,
	key string,
) string {
	raw, exists := externalIDs[key]
	if !exists {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

var _ interfaces.PaperCatalogProvider = (*semanticScholarProvider)(nil)
