package types

import (
	"errors"
	"fmt"
	"time"
)

var ErrInvalidDocumentStageEnvelope = errors.New("invalid document stage envelope")

// DocumentStage identifies one independently scheduled document workflow.
// These values are persisted and carried by task payloads; keep them stable.
type DocumentStage string

const (
	DocumentStageMarkdown           DocumentStage = "markdown"
	DocumentStageMetadataExtraction DocumentStage = "metadata_extraction"
	DocumentStageSourceLookup       DocumentStage = "source_lookup"
	DocumentStageReferenceLookup    DocumentStage = "reference_lookup"
)

func (stage DocumentStage) Valid() bool {
	switch stage {
	case DocumentStageMarkdown, DocumentStageMetadataExtraction,
		DocumentStageSourceLookup, DocumentStageReferenceLookup:
		return true
	default:
		return false
	}
}

// DocumentStageStatus is the durable control state of one DocumentStage.
type DocumentStageStatus string

const (
	DocumentStageNotStarted DocumentStageStatus = "not_started"
	DocumentStageQueued     DocumentStageStatus = "queued"
	DocumentStageRunning    DocumentStageStatus = "running"
	DocumentStageSucceeded  DocumentStageStatus = "succeeded"
	DocumentStageFailed     DocumentStageStatus = "failed"
	DocumentStageCancelled  DocumentStageStatus = "cancelled"
	DocumentStageSkipped    DocumentStageStatus = "skipped"
)

func (status DocumentStageStatus) Valid() bool {
	switch status {
	case DocumentStageNotStarted, DocumentStageQueued, DocumentStageRunning,
		DocumentStageSucceeded, DocumentStageFailed, DocumentStageCancelled,
		DocumentStageSkipped:
		return true
	default:
		return false
	}
}

func (status DocumentStageStatus) Active() bool {
	return status == DocumentStageQueued || status == DocumentStageRunning
}

func (status DocumentStageStatus) Terminal() bool {
	return status == DocumentStageSucceeded || status == DocumentStageFailed ||
		status == DocumentStageCancelled || status == DocumentStageSkipped
}

// DocumentStageState is a stage snapshot returned by atomic repository
// transitions. Generation starts at zero and is incremented only when a new
// user-visible attempt is queued.
type DocumentStageState struct {
	Stage        DocumentStage       `json:"stage"`
	Status       DocumentStageStatus `json:"status"`
	Generation   int64               `json:"generation"`
	UpdatedAt    time.Time           `json:"updatedAt"`
	ErrorCode    *string             `json:"errorCode,omitempty"`
	ErrorMessage *string             `json:"errorMessage,omitempty"`
}

// ValidateDocumentStageEnvelope requires a complete durable stage fence.
func ValidateDocumentStageEnvelope(stage DocumentStage, generation int64, expected DocumentStage) error {
	if stage != expected {
		return fmt.Errorf("%w: stage must be %q", ErrInvalidDocumentStageEnvelope, expected)
	}
	if generation < 1 {
		return fmt.Errorf("%w: generation must be positive", ErrInvalidDocumentStageEnvelope)
	}
	return nil
}

func HasDocumentStageEnvelope(stage DocumentStage, generation int64) bool {
	return stage.Valid() && generation > 0
}

// StageState returns the flattened stage fields from a Document.
func (document Document) StageState(stage DocumentStage) (DocumentStageState, error) {
	state := DocumentStageState{Stage: stage}
	switch stage {
	case DocumentStageMarkdown:
		state.Status, state.Generation, state.UpdatedAt = document.MarkdownStatus, document.MarkdownGeneration, document.MarkdownUpdatedAt
		state.ErrorCode, state.ErrorMessage = document.MarkdownErrorCode, document.MarkdownErrorMessage
	case DocumentStageMetadataExtraction:
		state.Status, state.Generation, state.UpdatedAt = document.MetadataExtractionStatus, document.MetadataExtractionGeneration, document.MetadataExtractionUpdatedAt
		state.ErrorCode, state.ErrorMessage = document.MetadataExtractionErrorCode, document.MetadataExtractionErrorMessage
	case DocumentStageSourceLookup:
		state.Status, state.Generation, state.UpdatedAt = document.SourceLookupStatus, document.SourceLookupGeneration, document.SourceLookupUpdatedAt
		state.ErrorCode, state.ErrorMessage = document.SourceLookupErrorCode, document.SourceLookupErrorMessage
	case DocumentStageReferenceLookup:
		state.Status, state.Generation, state.UpdatedAt = document.ReferenceLookupStatus, document.ReferenceLookupGeneration, document.ReferenceLookupUpdatedAt
		state.ErrorCode, state.ErrorMessage = document.ReferenceLookupErrorCode, document.ReferenceLookupErrorMessage
	default:
		return DocumentStageState{}, fmt.Errorf("invalid document stage %q", stage)
	}
	return state, nil
}
