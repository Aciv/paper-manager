package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

// PaperCatalogResolveCommand is the typed command for exactly one stored
// document. Source tasks use the configured provider fan-out. Reference tasks
// claim a provider and fallback state from PaperReferenceScheduler instead of
// carrying either concern in the Asynq payload.
type PaperCatalogResolveCommand struct {
	RequestID   string
	DocumentID  uuid.UUID
	IsReference bool
	// MarkdownSHA256 is an in-process optional revision guard retained for
	// direct service callers. It is intentionally never serialized into the
	// catalog task payload.
	MarkdownSHA256 string
	Attempt        int
}

// PaperReferenceDispatchCommand is the typed request to build a bounded,
// provider-partitioned bibliography lookup run from persisted citations. It is
// intentionally separate from PaperCatalogResolveCommand: dispatch schedules
// work, whereas catalog resolution executes one source or provider batch.
type PaperReferenceDispatchCommand struct {
	RequestID      string
	DocumentID     uuid.UUID
	MarkdownSHA256 string
	Attempt        int
	ReferenceRunID uuid.UUID
}

// Validate verifies that the dispatcher has one durable source revision and
// a stable run ID for idempotent queueing and cancellation.
func (command PaperReferenceDispatchCommand) Validate() error {
	if command.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidPaperCatalogData)
	}
	if err := validateSHA256("markdown_sha256", command.MarkdownSHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPaperCatalogData, err)
	}
	if command.Attempt < 0 {
		return fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperCatalogData)
	}
	if command.ReferenceRunID == uuid.Nil {
		return fmt.Errorf("%w: reference_run_id is required", ErrInvalidPaperCatalogData)
	}
	if strings.TrimSpace(command.MarkdownSHA256) != "" {
		if err := validateSHA256("markdown_sha256", command.MarkdownSHA256); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPaperCatalogData, err)
		}
	}
	if command.Attempt < 0 {
		return fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperCatalogData)
	}
	return nil
}

// Validate verifies the one target document identity.
func (command PaperCatalogResolveCommand) Validate() error {
	if command.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidPaperCatalogData)
	}
	return nil
}

// PaperLookupQuery is the provider-neutral query constructed from one
// extraction result. SourceURL supports exact provider-native lookups, such as
// an arXiv abstract or PDF URL.
type PaperLookupQuery struct {
	DOI           string   `json:"doi,omitempty"`
	Title         string   `json:"title,omitempty"`
	SourceURL     string   `json:"source_url,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	PublishedYear int      `json:"published_year,omitempty"`
	Venue         string   `json:"venue,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

// Validate checks provider-independent query requirements.
func (query PaperLookupQuery) Validate() error {
	if strings.TrimSpace(query.DOI) == "" &&
		strings.TrimSpace(query.Title) == "" &&
		strings.TrimSpace(query.SourceURL) == "" {
		return fmt.Errorf("%w: DOI, title, or source URL is required", ErrInvalidPaperCatalogData)
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
	if math.IsNaN(candidate.ProviderScore) ||
		math.IsInf(candidate.ProviderScore, 0) ||
		candidate.ProviderScore < 0 ||
		candidate.ProviderScore > 1 {
		return fmt.Errorf("%w: provider_score must be between 0 and 1", ErrInvalidPaperCatalogData)
	}
	if math.IsNaN(candidate.MatchScore) ||
		math.IsInf(candidate.MatchScore, 0) ||
		candidate.MatchScore < 0 ||
		candidate.MatchScore > 1 {
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
