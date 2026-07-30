package types

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidPaperInfoTaskPayload indicates that a paper-information task
// cannot be safely converted into a typed application command.
var ErrInvalidPaperInfoTaskPayload = errors.New("invalid paper info task payload")

// PaperMetadataExtractPayload is the durable queue message for candidate
// extraction. It intentionally contains no Markdown, PDF bytes, Blocks, or
// provider response data; the worker reloads all inputs from durable storage.
type PaperMetadataExtractPayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document.
	DocumentID string `json:"document_id"`
	// MarkdownSHA256 pins the task to one parsed Markdown revision.
	MarkdownSHA256 string `json:"markdown_sha256"`
	// Attempt identifies one user-initiated paper-info run. Queue retries retain
	// the same attempt.
	Attempt int `json:"attempt,omitempty"`
}

// PaperReferenceDispatchPayload is the durable queue message that turns the
// already-persisted citation snapshot for one Markdown revision into bounded,
// provider-assigned catalog tasks. It deliberately carries neither reference
// text nor provider names: both are loaded or selected by the dispatcher.
type PaperReferenceDispatchPayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document that owns the bibliography.
	DocumentID string `json:"document_id"`
	// MarkdownSHA256 pins dispatch to the citation snapshot extracted from this
	// parsed Markdown revision.
	MarkdownSHA256 string `json:"markdown_sha256"`
	// Attempt identifies one user-initiated paper-information run. Queue
	// retries retain the same value and therefore retain the same run ID.
	Attempt int `json:"attempt,omitempty"`
	// ReferenceRunID is a stable ID for this revision/attempt. It binds every
	// batch and fallback emitted by the dispatcher to one cancellable run.
	ReferenceRunID string `json:"reference_run_id"`
}

// PaperCatalogResolvePayload is the durable queue message for one external
// catalog lookup. Query inputs are always reloaded from durable storage; title,
// providers, fallback history, and reference-run ownership never enter Redis.
//
// When IsReference is false, DocumentID is the uploaded source paper. When it
// is true, DocumentID is one unresolved cited-paper placeholder. The global
// reference scheduler owns the placeholder's source/run/provider association.
type PaperCatalogResolvePayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies exactly one document to query.
	DocumentID string `json:"document_id"`
	// Attempt is the owning pipeline attempt for source work. Reference work
	// obtains its attempt from the scheduler owner.
	Attempt int `json:"attempt,omitempty"`
	// IsReference selects scheduler-owned reference lookup instead of the
	// independent source-paper provider fan-out.
	IsReference bool `json:"is_reference,omitempty"`
}

// Validate verifies that the extraction payload is safe to enqueue.
func (p PaperMetadataExtractPayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented payload into an application command.
func (p PaperMetadataExtractPayload) Command() (PaperMetadataExtractCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperMetadataExtractCommand{}, err
	}
	if err := validateSHA256("markdown_sha256", p.MarkdownSHA256); err != nil {
		return PaperMetadataExtractCommand{}, fmt.Errorf("%w: %v", ErrInvalidPaperInfoTaskPayload, err)
	}
	if p.Attempt < 0 {
		return PaperMetadataExtractCommand{}, fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperInfoTaskPayload)
	}
	return PaperMetadataExtractCommand{
		RequestID:      strings.TrimSpace(p.RequestID),
		DocumentID:     documentID,
		MarkdownSHA256: strings.ToLower(strings.TrimSpace(p.MarkdownSHA256)),
		Attempt:        p.Attempt,
	}, nil
}

// Validate verifies that the dispatcher payload can be converted to its
// typed application command.
func (p PaperReferenceDispatchPayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented dispatcher payload into an application
// command. The dispatcher only accepts a concrete Markdown revision so stale
// extraction retries cannot schedule a newer bibliography accidentally.
func (p PaperReferenceDispatchPayload) Command() (PaperReferenceDispatchCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperReferenceDispatchCommand{}, err
	}
	if err := validateSHA256("markdown_sha256", p.MarkdownSHA256); err != nil {
		return PaperReferenceDispatchCommand{}, fmt.Errorf("%w: %v", ErrInvalidPaperInfoTaskPayload, err)
	}
	if p.Attempt < 0 {
		return PaperReferenceDispatchCommand{}, fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperInfoTaskPayload)
	}
	runID, err := parsePaperTaskUUID("reference_run_id", p.ReferenceRunID)
	if err != nil {
		return PaperReferenceDispatchCommand{}, err
	}
	return PaperReferenceDispatchCommand{
		RequestID:      strings.TrimSpace(p.RequestID),
		DocumentID:     documentID,
		MarkdownSHA256: strings.ToLower(strings.TrimSpace(p.MarkdownSHA256)),
		Attempt:        p.Attempt,
		ReferenceRunID: runID,
	}, nil
}

// Validate verifies that the catalog payload is safe to enqueue.
func (p PaperCatalogResolvePayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented payload into an application command.
func (p PaperCatalogResolvePayload) Command() (PaperCatalogResolveCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperCatalogResolveCommand{}, err
	}
	return PaperCatalogResolveCommand{
		RequestID:   strings.TrimSpace(p.RequestID),
		DocumentID:  documentID,
		IsReference: p.IsReference,
		Attempt:     p.Attempt,
	}, nil
}

func parsePaperTaskUUID(fieldName, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a non-zero UUID", ErrInvalidPaperInfoTaskPayload, fieldName)
	}
	return id, nil
}
