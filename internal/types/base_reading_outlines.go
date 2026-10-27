package types

import "github.com/google/uuid"

// BaseReadingOutlineRow is a private batch projection, never an API response.
// Stored JSON may contain storage keys; only the service's sanitized view is public.
type BaseReadingOutlineRow struct {
	DocumentID         uuid.UUID
	Title              string
	SourceState        DocumentSourceState
	MarkdownStatus     DocumentStageStatus
	MarkdownGeneration int64
	SourceGeneration   *int64
	SourceOutlineJSON  string
	NoteRevision       *int64
	NoteSizeBytes      int64
	NoteStructureJSON  string
}

type BaseReadingOutlinesQuery struct {
	Page, PageSize, SectionLimit int
	OutlineType                  string
}

type OutlineAvailability struct {
	State         string `json:"state"`
	ErrorCode     string `json:"error_code,omitempty"`
	TotalSections int    `json:"total_sections"`
	Complete      bool   `json:"complete"`
}

type BaseSourceOutline struct {
	OutlineAvailability
	SchemaVersion int                      `json:"schema_version"`
	Generation    int64                    `json:"generation"`
	Source        DocumentOutlineSource    `json:"source,omitempty"`
	Sections      []DocumentOutlineSection `json:"sections"`
}

type BaseNotesOutline struct {
	OutlineAvailability
	Revision           int64                     `json:"revision"`
	DescriptionsLoaded bool                      `json:"descriptions_loaded"`
	Sections           []ReadingNoteSectionEntry `json:"sections"`
}

type BaseReadingOutlineItem struct {
	DocumentID     uuid.UUID           `json:"document_id"`
	Title          string              `json:"title"`
	MarkdownStatus DocumentStageStatus `json:"markdown_status"`
	Status         string              `json:"status"`
	SourceOutline  *BaseSourceOutline  `json:"source_outline,omitempty"`
	NotesOutline   *BaseNotesOutline   `json:"notes_outline,omitempty"`
}

type BaseOutlineSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Partial   int `json:"partial"`
	Failed    int `json:"failed"`
}

type BaseReadingOutlinesPage struct {
	BaseID      uuid.UUID                `json:"base_id"`
	Scope       string                   `json:"scope"`
	Status      string                   `json:"status"`
	OutlineType string                   `json:"outline_type"`
	Page        int                      `json:"page"`
	PageSize    int                      `json:"page_size"`
	Total       int64                    `json:"total"`
	HasMore     bool                     `json:"has_more"`
	Summary     BaseOutlineSummary       `json:"summary"`
	Items       []BaseReadingOutlineItem `json:"items"`
}
