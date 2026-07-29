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

// PaperCatalogScope identifies whether a catalog task resolves the uploaded
// source paper or one of its outgoing references. Both scopes deliberately use
// the same task type: they share provider policy while reference tasks remain
// schedulable on the low-priority queue.
type PaperCatalogScope string

const (
	PaperCatalogScopeSource    PaperCatalogScope = "source"
	PaperCatalogScopeReference PaperCatalogScope = "reference"
)

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

// PaperCatalogResolvePayload is the durable queue message for external catalog
// lookup. Query inputs are reloaded from paper_metadata and paper_citations;
// title, authors, references, and provider responses never enter Redis.
type PaperCatalogResolvePayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document.
	DocumentID string `json:"document_id"`
	// MarkdownSHA256 rejects stale work when lookup inputs came from parsed
	// Markdown. It is omitted for metadata-only lookup and unknown placeholders.
	MarkdownSHA256 string `json:"markdown_sha256,omitempty"`
	// Attempt identifies one user-initiated paper-info run. Queue retries retain
	// the same attempt.
	Attempt int `json:"attempt,omitempty"`
	// Scope defaults to source for compatibility with tasks enqueued before
	// reference work was split out.
	Scope PaperCatalogScope `json:"scope,omitempty"`
	// CitedDocumentID identifies the unknown placeholder to resolve when Scope
	// is reference. The citing source remains DocumentID.
	CitedDocumentID string `json:"cited_document_id,omitempty"`
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
	if strings.TrimSpace(p.MarkdownSHA256) != "" {
		if err := validateSHA256("markdown_sha256", p.MarkdownSHA256); err != nil {
			return PaperCatalogResolveCommand{}, fmt.Errorf("%w: %v", ErrInvalidPaperInfoTaskPayload, err)
		}
	}
	if p.Attempt < 0 {
		return PaperCatalogResolveCommand{}, fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperInfoTaskPayload)
	}
	scope := p.Scope
	if scope == "" {
		scope = PaperCatalogScopeSource
	}
	if scope != PaperCatalogScopeSource && scope != PaperCatalogScopeReference {
		return PaperCatalogResolveCommand{}, fmt.Errorf("%w: unsupported catalog scope %q", ErrInvalidPaperInfoTaskPayload, scope)
	}
	var citedDocumentID uuid.UUID
	if scope == PaperCatalogScopeReference {
		citedDocumentID, err = parsePaperTaskUUID("cited_document_id", p.CitedDocumentID)
		if err != nil {
			return PaperCatalogResolveCommand{}, err
		}
		if citedDocumentID == documentID {
			return PaperCatalogResolveCommand{}, fmt.Errorf("%w: cited_document_id must differ from document_id", ErrInvalidPaperInfoTaskPayload)
		}
	}
	return PaperCatalogResolveCommand{
		RequestID:       strings.TrimSpace(p.RequestID),
		DocumentID:      documentID,
		MarkdownSHA256:  strings.ToLower(strings.TrimSpace(p.MarkdownSHA256)),
		Attempt:         p.Attempt,
		Scope:           scope,
		CitedDocumentID: citedDocumentID,
	}, nil
}

func parsePaperTaskUUID(fieldName, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a non-zero UUID", ErrInvalidPaperInfoTaskPayload, fieldName)
	}
	return id, nil
}
