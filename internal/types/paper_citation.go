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
	CreatedAt       time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

// PaperCitationInput is a paper-domain write request. A nil CitedDocumentID
// means the extraction reference has not matched a stored paper yet, so
// PaperService must create or reuse an unknown placeholder.
type PaperCitationInput struct {
	CitedDocumentID *uuid.UUID `json:"citedDocumentId,omitempty"`
	RawReference    string     `json:"rawReference"`
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
	return nil
}
