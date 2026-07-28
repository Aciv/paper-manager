package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultPaperCatalogLimit is used when a provider query omits a limit.
	DefaultPaperCatalogLimit = 10
	// MaxPaperCatalogLimit bounds results returned by one provider.
	MaxPaperCatalogLimit = 50
)

// ErrInvalidPaperCatalogData indicates an unusable catalog query or candidate.
var ErrInvalidPaperCatalogData = errors.New("invalid paper catalog data")

// PaperCatalogStatus records only the terminal result of external catalog
// lookup and reference persistence. Queue lifecycle belongs to Asynq.
type PaperCatalogStatus string

const (
	PaperCatalogStatusSuccess PaperCatalogStatus = "success"
	PaperCatalogStatusFailed  PaperCatalogStatus = "failed"
)

// IsValid reports whether status is a supported terminal result.
func (status PaperCatalogStatus) IsValid() bool {
	switch status {
	case PaperCatalogStatusSuccess, PaperCatalogStatusFailed:
		return true
	default:
		return false
	}
}

// PaperCatalogResolveCommand is the typed application command decoded from a
// queue payload. ExtractionID points to the durable query source.
type PaperCatalogResolveCommand struct {
	RequestID      string
	DocumentID     uuid.UUID
	MarkdownSHA256 string
	ExtractionID   uuid.UUID
	Attempt        int
}

// Validate verifies identifiers and revision information on the command.
func (command PaperCatalogResolveCommand) Validate() error {
	if command.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidPaperCatalogData)
	}
	if command.ExtractionID == uuid.Nil {
		return fmt.Errorf("%w: extraction_id is required", ErrInvalidPaperCatalogData)
	}
	if err := validateSHA256("markdown_sha256", command.MarkdownSHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPaperCatalogData, err)
	}
	if command.Attempt < 0 {
		return fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperCatalogData)
	}
	return nil
}

// PaperLookupQuery is the provider-neutral query constructed from one
// extraction result. At least DOI or title must be present.
type PaperLookupQuery struct {
	DOI           string   `json:"doi,omitempty"`
	Title         string   `json:"title,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	PublishedYear int      `json:"published_year,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

// Validate checks provider-independent query requirements.
func (query PaperLookupQuery) Validate() error {
	if strings.TrimSpace(query.DOI) == "" && strings.TrimSpace(query.Title) == "" {
		return fmt.Errorf("%w: DOI or title is required", ErrInvalidPaperCatalogData)
	}
	if query.PublishedYear < 0 {
		return fmt.Errorf("%w: published year cannot be negative", ErrInvalidPaperCatalogData)
	}
	if query.Limit < 0 || query.Limit > MaxPaperCatalogLimit {
		return fmt.Errorf("%w: limit must be between 0 and %d", ErrInvalidPaperCatalogData, MaxPaperCatalogLimit)
	}
	for index, author := range query.Authors {
		if strings.TrimSpace(author) == "" {
			return fmt.Errorf("%w: author %d is empty", ErrInvalidPaperCatalogData, index)
		}
	}
	return nil
}

// EffectiveLimit returns the default result limit when Limit is zero.
func (query PaperLookupQuery) EffectiveLimit() int {
	if query.Limit == 0 {
		return DefaultPaperCatalogLimit
	}
	return query.Limit
}

// PaperCatalogCandidate is one provider response normalized for matching.
// ProviderScore and MatchScore are both normalized to [0,1]; ProviderScore is
// supplied by the adapter, while MatchScore is assigned by the Go resolver.
type PaperCatalogCandidate struct {
	Provider      string        `json:"provider"`
	ProviderID    string        `json:"provider_id,omitempty"`
	DOI           string        `json:"doi,omitempty"`
	Title         string        `json:"title"`
	Abstract      string        `json:"abstract,omitempty"`
	Authors       []PaperAuthor `json:"authors"`
	Tags          []string      `json:"tags"`
	SourceName    string        `json:"source_name,omitempty"`
	SourceURL     string        `json:"source_url,omitempty"`
	Venue         string        `json:"venue,omitempty"`
	PublishedAt   *time.Time    `json:"published_at,omitempty"`
	Volume        string        `json:"volume,omitempty"`
	Issue         string        `json:"issue,omitempty"`
	Pages         string        `json:"pages,omitempty"`
	Language      string        `json:"language,omitempty"`
	ProviderScore float64       `json:"provider_score,omitempty"`
	MatchScore    float64       `json:"match_score,omitempty"`
	RawMetadata   JSON          `json:"raw_metadata,omitempty"`
	RetrievedAt   time.Time     `json:"retrieved_at"`
}

// Validate checks required provider identity and normalized score ranges.
func (candidate PaperCatalogCandidate) Validate() error {
	if strings.TrimSpace(candidate.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidPaperCatalogData)
	}
	if strings.TrimSpace(candidate.ProviderID) == "" && strings.TrimSpace(candidate.DOI) == "" {
		return fmt.Errorf("%w: provider_id or DOI is required", ErrInvalidPaperCatalogData)
	}
	if strings.TrimSpace(candidate.Title) == "" {
		return fmt.Errorf("%w: title is required", ErrInvalidPaperCatalogData)
	}
	if candidate.ProviderScore < 0 || candidate.ProviderScore > 1 {
		return fmt.Errorf("%w: provider_score must be between 0 and 1", ErrInvalidPaperCatalogData)
	}
	if candidate.MatchScore < 0 || candidate.MatchScore > 1 {
		return fmt.Errorf("%w: match_score must be between 0 and 1", ErrInvalidPaperCatalogData)
	}
	if len(candidate.RawMetadata) > 0 && !json.Valid(candidate.RawMetadata) {
		return fmt.Errorf("%w: raw_metadata must be valid JSON", ErrInvalidPaperCatalogData)
	}
	for index, author := range candidate.Authors {
		if strings.TrimSpace(author.Name) == "" {
			return fmt.Errorf("%w: author %d name is required", ErrInvalidPaperCatalogData, index)
		}
	}
	for index, tag := range candidate.Tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("%w: tag %d is empty", ErrInvalidPaperCatalogData, index)
		}
	}
	return nil
}
