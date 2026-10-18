package types

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

var (
	// ErrInvalidPaperCitation means one of the relation endpoints is missing.
	ErrInvalidPaperCitation = errors.New("paper citation requires a citing document and a reference")
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

// PaperCitation is one directional bibliography entry owned by a stored
// document. The embedded PaperReference carries the bibliographic metadata and
// resolution state; CitedDocumentID is the single resolved target and stays nil
// until a local or catalog match is found.
type PaperCitation struct {
	PaperReference `gorm:"embedded"`
	// Origin is internal merge state and is deliberately not exposed by the
	// public citation API.
	Origin PaperCitationOrigin `gorm:"column:origin;type:text;not null;default:'manual'" json:"-"`
}

// PaperCitationInput is a paper-domain write request. A nil CitedDocumentID
// means the reference has not matched a stored paper yet; PaperService creates
// a reference row owned by this citing paper.
type PaperCitationInput struct {
	CitedDocumentID *uuid.UUID `json:"citedDocumentId,omitempty"`
	RawReference    string     `json:"rawReference"`
	// Candidate is internal extraction evidence for a reference row. It is not
	// accepted from the public citation API.
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
	if citation.CitingDocumentID == uuid.Nil || citation.ReferenceID == uuid.Nil {
		return ErrInvalidPaperCitation
	}
	if citation.CitedDocumentID != nil && *citation.CitedDocumentID == citation.CitingDocumentID {
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
	if err := citation.PaperReference.Validate(); err != nil {
		return err
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
