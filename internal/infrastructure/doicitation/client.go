package doicitation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

var (
	errUnsafeRedirect   = errors.New("unsafe DOI redirect target")
	errTooManyRedirects = errors.New("too many DOI redirects")
)

var doiContentNegotiationHosts = map[string]struct{}{
	"doi.org":            {},
	"api.crossref.org":   {},
	"data.crosscite.org": {},
}

type cacheEntry struct {
	citation types.DOICitation
	expires  time.Time
}

type inflightResult struct {
	citation types.DOICitation
	err      error
}

type inflightRequest struct {
	done   chan struct{}
	result inflightResult
}

type client struct {
	baseURL      *url.URL
	httpClient   *http.Client
	timeout      time.Duration
	maxBodyBytes int64
	cacheTTL     time.Duration
	cacheLimit   int
	userAgent    string

	mu       sync.Mutex
	cache    map[string]cacheEntry
	order    []string
	inflight map[string]*inflightRequest
}

func NewClient(cfg config.Config) interfaces.DOICitationClient {
	baseURL, _ := url.Parse("https://doi.org")
	return newClient(baseURL, nil, cfg.BibliographyDOITimeout,
		cfg.BibliographyDOIMaxBodyBytes, cfg.BibliographyDOICacheTTL,
		cfg.BibliographyDOICacheEntries, cfg.PaperCatalogUserAgent)
}

func newClient(
	baseURL *url.URL, httpClient *http.Client, timeout time.Duration,
	maxBodyBytes int64, cacheTTL time.Duration, cacheLimit int, userAgent string,
) *client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = 256 << 10
	}
	if cacheTTL <= 0 {
		cacheTTL = 30 * time.Minute
	}
	if cacheLimit <= 0 {
		cacheLimit = 100
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = "papersManager/1.0"
	}
	allowedHosts := make(map[string]struct{}, len(doiContentNegotiationHosts)+1)
	for host := range doiContentNegotiationHosts {
		allowedHosts[host] = struct{}{}
	}
	allowedHosts[strings.ToLower(baseURL.Hostname())] = struct{}{}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	copyClient := *httpClient
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errTooManyRedirects
		}
		if request.URL.Scheme != "https" {
			return errUnsafeRedirect
		}
		if _, allowed := allowedHosts[strings.ToLower(request.URL.Hostname())]; !allowed {
			return errUnsafeRedirect
		}
		return nil
	}
	return &client{
		baseURL: baseURL, httpClient: &copyClient, timeout: timeout,
		maxBodyBytes: maxBodyBytes, cacheTTL: cacheTTL, cacheLimit: cacheLimit,
		userAgent: userAgent, cache: make(map[string]cacheEntry),
		inflight: make(map[string]*inflightRequest),
	}
}

func (c *client) FetchBibTeX(ctx context.Context, rawDOI string) (types.DOICitation, error) {
	doi := types.NormalizeDOI(rawDOI)
	if doi == "" {
		return types.DOICitation{}, &types.DOICitationError{Code: "invalid_doi", Err: fmt.Errorf("invalid DOI")}
	}
	key := strings.ToLower(doi)
	now := time.Now()
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok && now.Before(cached.expires) {
		c.mu.Unlock()
		return cached.citation, nil
	}
	if pending, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return types.DOICitation{}, citationContextError(ctx.Err())
		case <-pending.done:
			return pending.result.citation, pending.result.err
		}
	}
	pending := &inflightRequest{done: make(chan struct{})}
	c.inflight[key] = pending
	c.mu.Unlock()

	citation, err := c.fetch(ctx, doi)
	c.mu.Lock()
	pending.result = inflightResult{citation: citation, err: err}
	delete(c.inflight, key)
	if err == nil {
		c.storeLocked(key, citation, time.Now().Add(c.cacheTTL))
	}
	close(pending.done)
	c.mu.Unlock()
	return citation, err
}

func (c *client) fetch(parent context.Context, doi string) (types.DOICitation, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	endpoint := *c.baseURL
	segments := strings.Split(doi, "/")
	encoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		encoded = append(encoded, url.PathEscape(segment))
	}
	endpoint.RawPath = strings.TrimRight(endpoint.EscapedPath(), "/") + "/" + strings.Join(encoded, "/")
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/" + doi
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return types.DOICitation{}, &types.DOICitationError{Code: "request_invalid", Err: err}
	}
	request.Header.Set("Accept", "application/x-bibtex")
	request.Header.Set("User-Agent", c.userAgent)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return types.DOICitation{}, citationContextError(ctx.Err())
		}
		if errors.Is(err, errUnsafeRedirect) {
			return types.DOICitation{}, &types.DOICitationError{Code: "unsafe_redirect", Err: err}
		}
		if errors.Is(err, errTooManyRedirects) {
			return types.DOICitation{}, &types.DOICitationError{Code: "too_many_redirects", Err: err}
		}
		return types.DOICitation{}, &types.DOICitationError{Code: "network", Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return types.DOICitation{}, &types.DOICitationError{
			Code: fmt.Sprintf("http_%d", response.StatusCode), StatusCode: response.StatusCode,
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.maxBodyBytes+1))
	if err != nil {
		return types.DOICitation{}, &types.DOICitationError{Code: "read", Err: err}
	}
	if int64(len(body)) > c.maxBodyBytes {
		return types.DOICitation{}, &types.DOICitationError{Code: "body_too_large", Err: fmt.Errorf("BibTeX response exceeds %d bytes", c.maxBodyBytes)}
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	text := strings.TrimSpace(string(body))
	if strings.Contains(contentType, "text/html") || strings.HasPrefix(strings.ToLower(text), "<!doctype html") || strings.HasPrefix(strings.ToLower(text), "<html") {
		return types.DOICitation{}, &types.DOICitationError{Code: "html_response", Err: fmt.Errorf("DOI resolver returned HTML")}
	}
	if text == "" {
		return types.DOICitation{}, &types.DOICitationError{Code: "empty_response", Err: fmt.Errorf("DOI resolver returned an empty body")}
	}
	citationKey, err := inspectSingleBibTeXEntry(text)
	if err != nil {
		return types.DOICitation{}, &types.DOICitationError{Code: "invalid_bibtex", Err: err}
	}
	return types.DOICitation{BibTeX: text, CitationKey: citationKey}, nil
}

func citationContextError(err error) error {
	code := "cancelled"
	if err == context.DeadlineExceeded {
		code = "timeout"
	}
	return &types.DOICitationError{Code: code, Err: err}
}

func (c *client) storeLocked(key string, citation types.DOICitation, expires time.Time) {
	if _, exists := c.cache[key]; !exists {
		c.order = append(c.order, key)
	}
	c.cache[key] = cacheEntry{citation: citation, expires: expires}
	for len(c.cache) > c.cacheLimit && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, oldest)
	}
}

var _ interfaces.DOICitationClient = (*client)(nil)
