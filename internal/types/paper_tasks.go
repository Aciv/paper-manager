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
	// RestartSourceLookup asks a manual extraction retry to start a fresh
	// catalog generation only after the new extraction has been persisted.
	RestartSourceLookup bool `json:"restart_source_lookup,omitempty"`
}

// PaperReferenceQueryEnqueuePayload is the durable queue message that turns the
// already-persisted citation snapshot into bounded,
// provider-assigned catalog tasks. It deliberately carries neither reference
// text nor provider names: both are loaded or selected by the dispatcher.
type PaperReferenceQueryEnqueuePayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document that owns the bibliography.
	DocumentID string        `json:"document_id"`
	Stage      DocumentStage `json:"stage,omitempty"`
	Generation int64         `json:"generation,omitempty"`
}

// PaperSourceQueryEnqueuePayload is the durable queue message that starts one
// source-paper catalog lookup. Provider assignment, reference fallback, and
// query inputs never enter the Asynq payload; the in-process dispatcher owns
// them.
type PaperSourceQueryEnqueuePayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source paper whose source_lookup stage runs.
	DocumentID string        `json:"document_id,omitempty"`
	Stage      DocumentStage `json:"stage,omitempty"`
	Generation int64         `json:"generation,omitempty"`
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
		RequestID:           strings.TrimSpace(p.RequestID),
		DocumentID:          documentID,
		Stage:               p.Stage,
		Generation:          p.Generation,
		RestartSourceLookup: p.RestartSourceLookup,
	}, nil
}

// Validate verifies that the dispatcher payload can be converted to its
// typed application command.
func (p PaperReferenceQueryEnqueuePayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented dispatcher payload into an application
// command.
func (p PaperReferenceQueryEnqueuePayload) Command() (PaperReferenceQueryEnqueueCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperReferenceQueryEnqueueCommand{}, err
	}
	if err := ValidateDocumentStageEnvelope(p.Stage, p.Generation, DocumentStageReferenceLookup); err != nil {
		return PaperReferenceQueryEnqueueCommand{}, fmt.Errorf("%w: %w", ErrInvalidPaperInfoTaskPayload, err)
	}
	return PaperReferenceQueryEnqueueCommand{
		RequestID: strings.TrimSpace(p.RequestID), DocumentID: documentID,
		Stage: p.Stage, Generation: p.Generation,
	}, nil
}

// Validate verifies that the catalog payload is safe to enqueue.
func (p PaperSourceQueryEnqueuePayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented payload into an application command.
func (p PaperSourceQueryEnqueuePayload) Command() (PaperSourceQueryEnqueueCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperSourceQueryEnqueueCommand{}, err
	}
	if err := ValidateDocumentStageEnvelope(p.Stage, p.Generation, DocumentStageSourceLookup); err != nil {
		return PaperSourceQueryEnqueueCommand{}, fmt.Errorf("%w: %w", ErrInvalidPaperInfoTaskPayload, err)
	}
	return PaperSourceQueryEnqueueCommand{
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
