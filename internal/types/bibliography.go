package types

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrBibliographyBaseTooLarge    = errors.New("document base exceeds bibliography export limit")
	ErrBibliographyOutputTooLarge  = errors.New("generated bibliography exceeds output limit")
	ErrBibliographyTaskNotFound    = errors.New("bibliography generation task not found")
	ErrBibliographyTaskUnavailable = errors.New("bibliography task inspection is unavailable")
)

const (
	BibliographyKindPaper         = "paper"
	BibliographyExportSourceDOI   = "DOI"
	BibliographyExportSourceAuto  = "auto"
	BibliographyExportKindArticle = "article"
)

type BibliographySource struct {
	DocumentID  uuid.UUID
	DOI         string
	SourceState DocumentSourceState
	SortOrder   int
	AddedAt     time.Time
	Metadata    PaperMetadata
}

type BibliographySnapshotSource struct {
	Base    DocumentBase
	Entries []BibliographySource
}

type DOICitation struct {
	BibTeX      string
	CitationKey string
}

type DOICitationError struct {
	Code       string
	StatusCode int
	Err        error
}

func (err *DOICitationError) Error() string {
	if err == nil {
		return ""
	}
	if err.StatusCode > 0 {
		return fmt.Sprintf("doi.org citation request returned HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf("doi.org citation request failed: %v", err.Err)
}

func (err *DOICitationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type BibliographyEntryOutcome string

const (
	BibliographyEntryGenerated BibliographyEntryOutcome = "generated"
	BibliographyEntrySkipped   BibliographyEntryOutcome = "skipped"
	BibliographyEntryFailed    BibliographyEntryOutcome = "failed"
)

type BibliographyEntrySource string

const (
	BibliographySourceDOIOriginal    BibliographyEntrySource = "doi_original"
	BibliographySourceLocalNoDOI     BibliographyEntrySource = "local_no_doi"
	BibliographySourceLocalDOIFailed BibliographyEntrySource = "local_doi_failed"
)

type BibliographyEntryReport struct {
	DocumentID    uuid.UUID                `json:"documentId"`
	Title         string                   `json:"title,omitempty"`
	Kind          string                   `json:"kind"`
	Outcome       BibliographyEntryOutcome `json:"outcome"`
	Source        BibliographyEntrySource  `json:"source,omitempty"`
	CitationKey   string                   `json:"citationKey,omitempty"`
	MissingFields []string                 `json:"missingFields"`
	Warnings      []string                 `json:"warnings"`
	DOIErrorCode  string                   `json:"doiErrorCode,omitempty"`
}

type BibliographyExportEntry struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	BibTeX string `json:"bibtex"`
}

type BibliographySnapshot struct {
	BaseID        uuid.UUID                 `json:"baseId"`
	BaseName      string                    `json:"baseName"`
	Kind          string                    `json:"kind"`
	Filename      string                    `json:"filename"`
	BibTeX        string                    `json:"bibtex"`
	Entries       []BibliographyEntryReport `json:"entries"`
	ExportEntries []BibliographyExportEntry `json:"exportEntries"`
	Generated     int                       `json:"generated"`
	Skipped       int                       `json:"skipped"`
	Failed        int                       `json:"failed"`
}

type BibliographyTaskPayload struct {
	RequestID      string `json:"request_id,omitempty"`
	DocumentBaseID string `json:"document_base_id"`
	RunID          string `json:"run_id"`
	Attempt        int    `json:"attempt"`
}

func (payload BibliographyTaskPayload) Validate() error {
	if _, err := uuid.Parse(payload.DocumentBaseID); err != nil {
		return fmt.Errorf("invalid document base id: %w", err)
	}
	if _, err := uuid.Parse(payload.RunID); err != nil {
		return fmt.Errorf("invalid bibliography run id: %w", err)
	}
	if payload.Attempt < 1 {
		return errors.New("bibliography attempt must be positive")
	}
	return nil
}

type BibliographyTaskState string

const (
	BibliographyTaskQueued    BibliographyTaskState = "queued"
	BibliographyTaskRunning   BibliographyTaskState = "running"
	BibliographyTaskSucceeded BibliographyTaskState = "succeeded"
	BibliographyTaskFailed    BibliographyTaskState = "failed"
)

type BibliographyTask struct {
	ID        string                `json:"id"`
	BaseID    uuid.UUID             `json:"baseId"`
	State     BibliographyTaskState `json:"state"`
	Snapshot  *BibliographySnapshot `json:"snapshot,omitempty"`
	ErrorCode string                `json:"errorCode,omitempty"`
}
