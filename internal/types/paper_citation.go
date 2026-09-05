package types

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalidPaperCitation means one of the relation endpoints is missing.
	ErrInvalidPaperCitation = errors.New("paper citation requires two document IDs")
	// ErrSelfPaperCitation prevents a document from citing itself.
	ErrSelfPaperCitation = errors.New("paper citation cannot reference the same document")
)

// PaperCitationOrigin records whether a citation edge was supplied by a user
// or by the automatic paper extractor. Automatic refreshes may replace only
// extraction-origin edges; manual edges remain until an explicit user action.
type PaperCitationOrigin string

const (
	PaperCitationOriginManual     PaperCitationOrigin = "manual"
	PaperCitationOriginExtraction PaperCitationOrigin = "extraction"
)

// IsValid reports whether origin is a persisted citation origin.
func (origin PaperCitationOrigin) IsValid() bool {
	return origin == PaperCitationOriginManual || origin == PaperCitationOriginExtraction
}

// PaperCitation is a directional citation between two document records. The
// cited record may be an unknown bibliographic placeholder until catalog
// resolution matches it to a stored paper. RawReference preserves the exact
// extraction text that produced the edge.
type PaperCitation struct {
	// source document
	CitingDocumentID uuid.UUID `gorm:"column:citing_document_id;type:uuid;primaryKey" json:"citingDocumentId"`
	// cite document
	CitedDocumentID uuid.UUID `gorm:"column:cited_document_id;type:uuid;primaryKey" json:"citedDocumentId"`
	RawReference    string    `gorm:"column:raw_reference;type:text;not null" json:"rawReference"`
	// Origin is internal merge state and is deliberately not exposed by the
	// public citation API.
	Origin    PaperCitationOrigin `gorm:"column:origin;type:text;not null;default:'manual'" json:"-"`
	CreatedAt time.Time           `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

// PaperCitationInput is a paper-domain write request. A nil CitedDocumentID
// means the extraction reference has not matched a stored paper yet, so
// PaperService must create or reuse an unknown placeholder.
type PaperCitationInput struct {
	CitedDocumentID *uuid.UUID `json:"citedDocumentId,omitempty"`
	RawReference    string     `json:"rawReference"`
	// Candidate is internal extraction evidence for a newly created/reused
	// unknown placeholder. It is not accepted from the public citation API.
	Candidate *PaperReferenceCandidate `json:"-"`
}

// PaperCitationPage is an internal bounded page used by bibliography workers
// so large papers do not load every reference edge into memory at once.
type PaperCitationPage struct {
	Items      []PaperCitation
	NextOffset int
	HasMore    bool
}

// TableName returns the PostgreSQL table used for paper citation edges.
func (PaperCitation) TableName() string { return "paper_citations" }

// ValidateEndpoints rejects incomplete and self-referential citation endpoints.
// Read and delete operations do not need raw extraction evidence.
func (citation PaperCitation) ValidateEndpoints() error {
	if citation.CitingDocumentID == uuid.Nil || citation.CitedDocumentID == uuid.Nil {
		return ErrInvalidPaperCitation
	}
	if citation.CitingDocumentID == citation.CitedDocumentID {
		return ErrSelfPaperCitation
	}
	return nil
}

// Validate checks endpoints and the raw extraction evidence required by write
// operations.
func (citation PaperCitation) Validate() error {
	if err := citation.ValidateEndpoints(); err != nil {
		return err
	}
	if strings.TrimSpace(citation.RawReference) == "" {
		return ErrInvalidPaperCitation
	}
	if citation.Origin != "" && !citation.Origin.IsValid() {
		return ErrInvalidPaperCitation
	}
	return nil
}

// EffectiveOrigin treats rows written before citation origins were introduced
// as manual. That conservative upgrade rule prevents automatic retries from
// deleting historical user-created citations.
func (citation PaperCitation) EffectiveOrigin() PaperCitationOrigin {
	if citation.Origin == PaperCitationOriginExtraction {
		return PaperCitationOriginExtraction
	}
	return PaperCitationOriginManual
}

// PaperCitationReferenceKey normalizes raw citation text only for identity
// matching inside one bibliography. RawReference remains user-visible evidence.
func PaperCitationReferenceKey(rawReference string) string {
	return strings.ToLower(strings.Join(strings.Fields(rawReference), " "))
}
