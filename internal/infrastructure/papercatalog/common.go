// Package papercatalog implements normalized adapters for external scholarly
// metadata providers.
package papercatalog

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"paperMaster/internal/types"
)

const (
	defaultCatalogTimeout = 10 * time.Second
	maxCatalogBodyBytes   = 8 << 20
	defaultUserAgent      = "papersManager/1.0"
)

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// ProviderError classifies transport and HTTP failures for later Asynq retry
// policy without exposing response bodies or credentials.
type ProviderError struct {
	Provider   string
	StatusCode int
	RetryAfter time.Duration
	Retryable  bool
	Err        error
}

func (err *ProviderError) Error() string {
	if err == nil {
		return ""
	}
	if err.StatusCode != 0 {
		return fmt.Sprintf("%s catalog returned HTTP %d", err.Provider, err.StatusCode)
	}
	return fmt.Sprintf("%s catalog request failed: %v", err.Provider, err.Err)
}

func (err *ProviderError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// TaskRetryable exposes transport classification without coupling the queue
// layer to this concrete infrastructure package.
func (err *ProviderError) TaskRetryable() bool {
	return err != nil && err.Retryable
}

// TaskRetryAfter lets Asynq honor provider Retry-After responses.
func (err *ProviderError) TaskRetryAfter() time.Duration {
	if err == nil {
		return 0
	}
	return err.RetryAfter
}

type httpAdapter struct {
	provider  string
	baseURL   *url.URL
	client    *http.Client
	userAgent string
}

func newHTTPAdapter(
	provider string,
	baseURL string,
	client *http.Client,
	userAgent string,
) (httpAdapter, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return httpAdapter{}, fmt.Errorf("parse %s catalog base URL: %w", provider, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return httpAdapter{}, fmt.Errorf("%s catalog base URL must use http or https", provider)
	}
	if parsed.Host == "" {
		return httpAdapter{}, fmt.Errorf("%s catalog base URL requires a host", provider)
	}
	if client == nil {
		client = &http.Client{Timeout: defaultCatalogTimeout}
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = defaultUserAgent
	}
	return httpAdapter{
		provider:  provider,
		baseURL:   parsed,
		client:    client,
		userAgent: userAgent,
	}, nil
}

func (adapter httpAdapter) get(
	ctx context.Context,
	path string,
	query url.Values,
	headers http.Header,
) ([]byte, error) {
	endpoint := *adapter.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + strings.TrimLeft(path, "/")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build %s catalog request: %w", adapter.provider, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", adapter.userAgent)
	for key, values := range headers {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := adapter.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &ProviderError{
			Provider:  adapter.provider,
			Retryable: true,
			Err:       err,
		}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, &ProviderError{
			Provider:   adapter.provider,
			StatusCode: response.StatusCode,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
			Retryable: response.StatusCode == http.StatusRequestTimeout ||
				response.StatusCode == http.StatusTooManyRequests ||
				response.StatusCode >= http.StatusInternalServerError,
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBodyBytes+1))
	if err != nil {
		return nil, &ProviderError{
			Provider:  adapter.provider,
			Retryable: true,
			Err:       err,
		}
	}
	if len(body) > maxCatalogBodyBytes {
		return nil, &ProviderError{
			Provider:  adapter.provider,
			Retryable: false,
			Err:       errors.New("response exceeds catalog body limit"),
		}
	}
	return body, nil
}

func validateLookupQuery(query types.PaperLookupQuery) (types.PaperLookupQuery, error) {
	query.DOI = normalizeDOI(query.DOI)
	query.Title = normalizeSpace(query.Title)
	query.SourceURL = strings.TrimSpace(query.SourceURL)
	for index := range query.Authors {
		query.Authors[index] = normalizeSpace(query.Authors[index])
	}
	if err := query.Validate(); err != nil {
		return types.PaperLookupQuery{}, err
	}
	return query, nil
}

func requireDOIOrTitle(query types.PaperLookupQuery) error {
	if query.DOI == "" && query.Title == "" {
		return fmt.Errorf(
			"%w: provider requires DOI or title",
			types.ErrInvalidPaperCatalogData,
		)
	}
	return nil
}

func normalizeCandidates(
	candidates []types.PaperCatalogCandidate,
	limit int,
) ([]types.PaperCatalogCandidate, error) {
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for index := range candidates {
		candidate := &candidates[index]
		candidate.Provider = strings.TrimSpace(candidate.Provider)
		candidate.ProviderID = strings.TrimSpace(candidate.ProviderID)
		candidate.DOI = normalizeDOI(candidate.DOI)
		candidate.Title = normalizeSpace(candidate.Title)
		candidate.Abstract = normalizeSpace(candidate.Abstract)
		candidate.SourceName = normalizeSpace(candidate.SourceName)
		candidate.SourceURL = strings.TrimSpace(candidate.SourceURL)
		candidate.Venue = normalizeSpace(candidate.Venue)
		candidate.Volume = normalizeSpace(candidate.Volume)
		candidate.Issue = normalizeSpace(candidate.Issue)
		candidate.Pages = normalizeSpace(candidate.Pages)
		candidate.Language = strings.ToLower(strings.TrimSpace(candidate.Language))
		candidate.Authors = uniqueAuthors(candidate.Authors)
		candidate.Tags = uniqueStrings(candidate.Tags)
		if candidate.RetrievedAt.IsZero() {
			candidate.RetrievedAt = time.Now().UTC()
		}
		if err := candidate.Validate(); err != nil {
			return nil, fmt.Errorf("%s result %d: %w", candidate.Provider, index, err)
		}
	}
	return candidates, nil
}

func rankScore(index, total int) float64 {
	if index <= 0 || total <= 1 {
		return 1
	}
	return 1 - (float64(index) / float64(total))
}

func normalizeDOI(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	for _, prefix := range []string{
		"https://doi.org/",
		"http://doi.org/",
		"http://dx.doi.org/",
		"doi:",
	} {
		if strings.HasPrefix(lower, prefix) {
			value = value[len(prefix):]
			break
		}
	}
	return strings.TrimSpace(strings.TrimSuffix(value, "."))
}

func normalizeSpace(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(stripHTML(value))), " ")
}

func stripHTML(value string) string {
	return htmlTagPattern.ReplaceAllString(value, " ")
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizeSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueAuthors(values []types.PaperAuthor) []types.PaperAuthor {
	result := make([]types.PaperAuthor, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, author := range values {
		author.Name = normalizeSpace(author.Name)
		author.Affiliation = normalizeSpace(author.Affiliation)
		if author.Name == "" {
			continue
		}
		key := strings.ToLower(author.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, author)
	}
	return result
}

func parseDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.DateOnly, time.RFC3339, "2006-01"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func yearDate(year int) *time.Time {
	if year <= 0 {
		return nil
	}
	value := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	return &value
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}
