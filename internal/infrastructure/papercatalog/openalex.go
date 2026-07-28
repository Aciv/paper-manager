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
	OpenAlexProvider = "openalex"
	openAlexBaseURL  = "https://api.openalex.org"
	openAlexFields   = "id,doi,display_name,publication_date,publication_year,language,authorships,primary_location,topics,abstract_inverted_index"
)

type openAlexProvider struct {
	http   httpAdapter
	apiKey string
}

// NewOpenAlexProvider creates the production OpenAlex adapter. The API key is
// optional for low-volume use and enables OpenAlex's larger free daily budget.
func NewOpenAlexProvider(
	client *http.Client,
	apiKey string,
	userAgent string,
) (interfaces.PaperCatalogProvider, error) {
	return newOpenAlexProvider(
		openAlexBaseURL,
		client,
		apiKey,
		userAgent,
	)
}

func newOpenAlexProvider(
	baseURL string,
	client *http.Client,
	apiKey string,
	userAgent string,
) (*openAlexProvider, error) {
	adapter, err := newHTTPAdapter(
		OpenAlexProvider,
		baseURL,
		client,
		userAgent,
	)
	if err != nil {
		return nil, err
	}
	return &openAlexProvider{
		http:   adapter,
		apiKey: strings.TrimSpace(apiKey),
	}, nil
}

func (provider *openAlexProvider) Name() string {
	return OpenAlexProvider
}

func (provider *openAlexProvider) Search(
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
		"per-page": {strconv.Itoa(query.EffectiveLimit())},
		"select":   {openAlexFields},
	}
	if provider.apiKey != "" {
		values.Set("api_key", provider.apiKey)
	}
	if query.DOI != "" {
		values.Set("filter", "doi:https://doi.org/"+query.DOI)
	} else {
		values.Set("search", query.Title)
		filters := make([]string, 0, 2)
		if query.PublishedYear > 0 {
			filters = append(filters, "publication_year:"+strconv.Itoa(query.PublishedYear))
		}
		if len(query.Authors) > 0 {
			author := strings.ReplaceAll(query.Authors[0], `"`, "")
			filters = append(filters, `raw_author_name.search:"`+author+`"`)
		}
		if len(filters) > 0 {
			values.Set("filter", strings.Join(filters, ","))
		}
	}
	body, err := provider.http.get(ctx, "/works", values, nil)
	if err != nil {
		provider.logFailure(ctx, err)
		return nil, err
	}
	var response openAlexResponse
	if err := json.Unmarshal(body, &response); err != nil {
		err = fmt.Errorf("decode OpenAlex response: %w", err)
		provider.logFailure(ctx, err)
		return nil, err
	}
	retrievedAt := time.Now().UTC()
	candidates := make([]types.PaperCatalogCandidate, 0, len(response.Results))
	for index, work := range response.Results {
		if strings.TrimSpace(work.ID) == "" ||
			strings.TrimSpace(work.DisplayName) == "" {
			continue
		}
		candidates = append(candidates, work.candidate(
			rankScore(index, len(response.Results)),
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

func (provider *openAlexProvider) logFailure(ctx context.Context, err error) {
	logger.Error(ctx, "paper catalog search failed",
		"provider", provider.Name(),
		"error", err)
}

type openAlexResponse struct {
	Results []openAlexWork `json:"results"`
}

type openAlexWork struct {
	ID                    string           `json:"id"`
	DOI                   string           `json:"doi"`
	DisplayName           string           `json:"display_name"`
	PublicationDate       string           `json:"publication_date"`
	PublicationYear       int              `json:"publication_year"`
	Language              string           `json:"language"`
	Authorships           []openAlexAuthor `json:"authorships"`
	PrimaryLocation       openAlexLocation `json:"primary_location"`
	Topics                []openAlexTopic  `json:"topics"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
}

type openAlexAuthor struct {
	Author struct {
		DisplayName string `json:"display_name"`
	} `json:"author"`
	Institutions []struct {
		DisplayName string `json:"display_name"`
	} `json:"institutions"`
}

type openAlexLocation struct {
	LandingPageURL string `json:"landing_page_url"`
	Source         *struct {
		DisplayName string `json:"display_name"`
	} `json:"source"`
}

type openAlexTopic struct {
	DisplayName string `json:"display_name"`
}

func (work openAlexWork) candidate(
	score float64,
	retrievedAt time.Time,
) types.PaperCatalogCandidate {
	authors := make([]types.PaperAuthor, 0, len(work.Authorships))
	for _, authorship := range work.Authorships {
		affiliation := ""
		if len(authorship.Institutions) > 0 {
			affiliation = authorship.Institutions[0].DisplayName
		}
		authors = append(authors, types.PaperAuthor{
			Name:        authorship.Author.DisplayName,
			Affiliation: affiliation,
		})
	}
	tags := make([]string, 0, len(work.Topics))
	for _, topic := range work.Topics {
		tags = append(tags, topic.DisplayName)
	}
	sourceName := "OpenAlex"
	if work.PrimaryLocation.Source != nil &&
		strings.TrimSpace(work.PrimaryLocation.Source.DisplayName) != "" {
		sourceName = work.PrimaryLocation.Source.DisplayName
	}
	sourceURL := work.PrimaryLocation.LandingPageURL
	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = work.DOI
	}
	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = work.ID
	}
	publishedAt := parseDate(work.PublicationDate)
	if publishedAt == nil {
		publishedAt = yearDate(work.PublicationYear)
	}
	return types.PaperCatalogCandidate{
		Provider:      OpenAlexProvider,
		ProviderID:    openAlexID(work.ID),
		DOI:           work.DOI,
		Title:         work.DisplayName,
		Abstract:      openAlexAbstract(work.AbstractInvertedIndex),
		Authors:       authors,
		Tags:          tags,
		SourceName:    sourceName,
		SourceURL:     sourceURL,
		Venue:         sourceName,
		PublishedAt:   publishedAt,
		Language:      work.Language,
		ProviderScore: score,
		RetrievedAt:   retrievedAt,
	}
}

func openAlexID(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

func openAlexAbstract(index map[string][]int) string {
	const maxAbstractTokens = 100_000
	maxPosition := -1
	for _, positions := range index {
		for _, position := range positions {
			if position >= 0 && position < maxAbstractTokens && position > maxPosition {
				maxPosition = position
			}
		}
	}
	if maxPosition < 0 {
		return ""
	}
	tokens := make([]string, maxPosition+1)
	for token, positions := range index {
		for _, position := range positions {
			if position >= 0 && position < len(tokens) && tokens[position] == "" {
				tokens[position] = token
			}
		}
	}
	return strings.Join(tokens, " ")
}

var _ interfaces.PaperCatalogProvider = (*openAlexProvider)(nil)
