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
	DocumentID string        `json:"document_id"`
	Stage      DocumentStage `json:"stage,omitempty"`
	Generation int64         `json:"generation,omitempty"`
}

// PaperReferenceDispatchPayload is the durable queue message that turns the
// already-persisted citation snapshot into bounded,
// provider-assigned catalog tasks. It deliberately carries neither reference
// text nor provider names: both are loaded or selected by the dispatcher.
type PaperReferenceDispatchPayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document that owns the bibliography.
	DocumentID string        `json:"document_id"`
	Stage      DocumentStage `json:"stage,omitempty"`
	Generation int64         `json:"generation,omitempty"`
}

// PaperCatalogResolvePayload is the durable queue message for one external
// catalog lookup. Query inputs are always reloaded from durable storage; title,
// providers, fallback history, and reference-run ownership never enter Redis.
//
// ReferenceID selects shared-reference resolution; DocumentID is then only the
// origin citing document for logs/spans. Without ReferenceID, DocumentID is the
// source paper and Stage identifies its document stage.
type PaperCatalogResolvePayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source paper, or the origin citing document for
	// reference work.
	DocumentID string `json:"document_id,omitempty"`
	// ReferenceID selects the shared-reference resolution branch.
	ReferenceID string        `json:"reference_id,omitempty"`
	Stage       DocumentStage `json:"stage,omitempty"`
	Generation  int64         `json:"generation,omitempty"`
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
	if err := ValidateDocumentStageEnvelope(p.Stage, p.Generation, DocumentStageMetadataExtraction); err != nil {
		return PaperMetadataExtractCommand{}, fmt.Errorf("%w: %w", ErrInvalidPaperInfoTaskPayload, err)
	}
	return PaperMetadataExtractCommand{
		RequestID:  strings.TrimSpace(p.RequestID),
		DocumentID: documentID,
		Stage:      p.Stage, Generation: p.Generation,
	}, nil
}

// Validate verifies that the dispatcher payload can be converted to its
// typed application command.
func (p PaperReferenceDispatchPayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented dispatcher payload into an application
// command.
func (p PaperReferenceDispatchPayload) Command() (PaperReferenceDispatchCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperReferenceDispatchCommand{}, err
	}
	if err := ValidateDocumentStageEnvelope(p.Stage, p.Generation, DocumentStageReferenceLookup); err != nil {
		return PaperReferenceDispatchCommand{}, fmt.Errorf("%w: %w", ErrInvalidPaperInfoTaskPayload, err)
	}
	return PaperReferenceDispatchCommand{
		RequestID: strings.TrimSpace(p.RequestID), DocumentID: documentID,
		Stage: p.Stage, Generation: p.Generation,
	}, nil
}

// Validate verifies that the catalog payload is safe to enqueue.
func (p PaperCatalogResolvePayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented payload into an application command.
func (p PaperCatalogResolvePayload) Command() (PaperCatalogResolveCommand, error) {
	if strings.TrimSpace(p.ReferenceID) != "" {
		referenceID, err := parsePaperTaskUUID("reference_id", p.ReferenceID)
		if err != nil {
			return PaperCatalogResolveCommand{}, err
		}
		var documentID uuid.UUID
		if strings.TrimSpace(p.DocumentID) != "" {
			documentID, err = parsePaperTaskUUID("document_id", p.DocumentID)
			if err != nil {
				return PaperCatalogResolveCommand{}, err
			}
		}
		if p.Generation < 1 {
			return PaperCatalogResolveCommand{}, fmt.Errorf("%w: reference generation must be positive", ErrInvalidPaperInfoTaskPayload)
		}
		return PaperCatalogResolveCommand{
			RequestID:   strings.TrimSpace(p.RequestID),
			DocumentID:  documentID,
			ReferenceID: referenceID,
			Generation:  p.Generation,
		}, nil
	}
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperCatalogResolveCommand{}, err
	}
	if err := ValidateDocumentStageEnvelope(p.Stage, p.Generation, DocumentStageSourceLookup); err != nil {
		return PaperCatalogResolveCommand{}, fmt.Errorf("%w: %w", ErrInvalidPaperInfoTaskPayload, err)
	}
	return PaperCatalogResolveCommand{
		RequestID:  strings.TrimSpace(p.RequestID),
		DocumentID: documentID,
		Stage:      p.Stage, Generation: p.Generation,
	}, nil
}

func parsePaperTaskUUID(fieldName, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a non-zero UUID", ErrInvalidPaperInfoTaskPayload, fieldName)
	}
	return id, nil
}
