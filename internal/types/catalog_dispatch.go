package types

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// CatalogJobKind distinguishes the two provider-query priorities handled by
// the in-process catalog dispatcher.
type CatalogJobKind string

const (
	CatalogJobSource    CatalogJobKind = "source"
	CatalogJobReference CatalogJobKind = "reference"
)

// CatalogOutcome is the terminal disposition of one dispatcher job.
type CatalogOutcome string

const (
	CatalogOutcomeMatched CatalogOutcome = "matched"
	CatalogOutcomeNoMatch CatalogOutcome = "no_match"
	CatalogOutcomeFailed  CatalogOutcome = "failed"
)

// CatalogSourceJob asks the dispatcher to resolve one uploaded paper's
// canonical metadata. The query is built by the dispatch task so provider
// workers never read durable metadata themselves.
type CatalogSourceJob struct {
	RequestID  string
	DocumentID uuid.UUID
	Generation int64
	Query      PaperLookupQuery
}

// CatalogReferenceJob asks the dispatcher to resolve one citation row. The
// reference snapshot is the same row the dispatch task just queued; provider
// workers may patch it on completion but never create a second copy.
type CatalogReferenceJob struct {
	RequestID  string
	Reference  PaperReference
	Generation int64
	Query      PaperLookupQuery
}

// PaperQueryApplyPayload is the durable post-processing message emitted by
// the in-process dispatcher. It contains only one provider result (plus the
// final stage decision), never the whole provider history.
type PaperQueryApplyPayload struct {
	RequestID   string         `json:"request_id,omitempty"`
	Kind        CatalogJobKind `json:"kind"`
	DocumentID  string         `json:"document_id"`
	ReferenceID string         `json:"reference_id,omitempty"`
	// Stage lets read-only queue probes correlate a source result task with
	// its source_lookup stage. It is empty for reference results.
	Stage      DocumentStage          `json:"stage,omitempty"`
	Generation int64                  `json:"generation"`
	Provider   string                 `json:"provider,omitempty"`
	Final      bool                   `json:"final"`
	Outcome    CatalogOutcome         `json:"outcome,omitempty"`
	Candidate  *PaperCatalogCandidate `json:"candidate,omitempty"`
	// ResolvedDocumentID carries a local-library match found before any
	// provider request (reference jobs only).
	ResolvedDocumentID string `json:"resolved_document_id,omitempty"`
	ErrorCode          string `json:"error_code,omitempty"`
	ErrorMessage       string `json:"error_message,omitempty"`
	// Query is retained for source-result score comparison against the current
	// metadata record. It is small and avoids re-deriving a changed title.
	Query PaperLookupQuery `json:"query"`
}

// PaperQueryApplyCommand is the typed form of one post-processing task.
type PaperQueryApplyCommand struct {
	RequestID          string
	Kind               CatalogJobKind
	DocumentID         uuid.UUID
	ReferenceID        uuid.UUID
	Stage              DocumentStage
	Generation         int64
	Provider           string
	Final              bool
	Outcome            CatalogOutcome
	Candidate          *PaperCatalogCandidate
	ResolvedDocumentID uuid.UUID
	ErrorCode          string
	ErrorMessage       string
	Query              PaperLookupQuery
}

// Validate checks the durable result envelope.
func (p PaperQueryApplyPayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON payload into its application command.
func (p PaperQueryApplyPayload) Command() (PaperQueryApplyCommand, error) {
	kind := CatalogJobKind(strings.TrimSpace(string(p.Kind)))
	if kind != CatalogJobSource && kind != CatalogJobReference {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: catalog result kind is invalid", ErrInvalidPaperInfoTaskPayload)
	}
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperQueryApplyCommand{}, err
	}
	var referenceID uuid.UUID
	if strings.TrimSpace(p.ReferenceID) != "" {
		referenceID, err = parsePaperTaskUUID("reference_id", p.ReferenceID)
		if err != nil {
			return PaperQueryApplyCommand{}, err
		}
	}
	if kind == CatalogJobReference && referenceID == uuid.Nil {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: reference_id is required", ErrInvalidPaperInfoTaskPayload)
	}
	if kind == CatalogJobSource && referenceID != uuid.Nil {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: source result cannot carry reference_id", ErrInvalidPaperInfoTaskPayload)
	}
	if p.Generation < 1 {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: catalog result generation must be positive", ErrInvalidPaperInfoTaskPayload)
	}
	outcome := CatalogOutcome(strings.TrimSpace(string(p.Outcome)))
	if p.Final {
		if outcome != CatalogOutcomeMatched && outcome != CatalogOutcomeNoMatch && outcome != CatalogOutcomeFailed {
			return PaperQueryApplyCommand{}, fmt.Errorf("%w: final catalog result outcome is invalid", ErrInvalidPaperInfoTaskPayload)
		}
	} else if p.Candidate == nil {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: non-final catalog result requires a candidate", ErrInvalidPaperInfoTaskPayload)
	}
	var candidate *PaperCatalogCandidate
	if p.Candidate != nil {
		copied := *p.Candidate
		if err := copied.Validate(); err != nil {
			return PaperQueryApplyCommand{}, fmt.Errorf("%w: %w", ErrInvalidPaperInfoTaskPayload, err)
		}
		candidate = &copied
	}
	if p.Final && outcome == CatalogOutcomeMatched && candidate == nil && strings.TrimSpace(p.ResolvedDocumentID) == "" {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: matched catalog result requires a candidate or resolved document", ErrInvalidPaperInfoTaskPayload)
	}
	var resolvedDocumentID uuid.UUID
	if strings.TrimSpace(p.ResolvedDocumentID) != "" {
		resolvedDocumentID, err = parsePaperTaskUUID("resolved_document_id", p.ResolvedDocumentID)
		if err != nil {
			return PaperQueryApplyCommand{}, err
		}
	}
	if len(p.ErrorCode) > MaxPaperReferenceErrorCodeBytes ||
		len(p.ErrorMessage) > MaxPaperReferenceErrorMessageBytes {
		return PaperQueryApplyCommand{}, fmt.Errorf("%w: catalog result error exceeds persisted bound", ErrInvalidPaperInfoTaskPayload)
	}
	query := p.Query
	if kind == CatalogJobSource {
		if err := query.Validate(); err != nil {
			return PaperQueryApplyCommand{}, fmt.Errorf("%w: catalog result query: %w", ErrInvalidPaperInfoTaskPayload, err)
		}
	}
	return PaperQueryApplyCommand{
		RequestID: strings.TrimSpace(p.RequestID), Kind: kind,
		DocumentID: documentID, ReferenceID: referenceID, Stage: p.Stage, Generation: p.Generation,
		Provider: strings.TrimSpace(p.Provider), Final: p.Final, Outcome: outcome,
		Candidate: candidate, ResolvedDocumentID: resolvedDocumentID,
		ErrorCode: strings.TrimSpace(p.ErrorCode), ErrorMessage: strings.TrimSpace(p.ErrorMessage),
		Query: query,
	}, nil
}
