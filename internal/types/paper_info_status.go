package types

import "github.com/google/uuid"

// PaperInfoStageState is the observable state of one asynchronous paper-info
// stage. Queue-backed states are intentionally separate from document parsing:
// paper extraction or catalog lookup never changes Document.Status.
type PaperInfoStageState string

const (
	// PaperInfoStageIdle means no queued task or durable successful result is
	// observable. It does not claim that a previous automatic attempt failed.
	PaperInfoStageIdle PaperInfoStageState = "idle"
	// PaperInfoStageQueued includes Asynq pending, scheduled, retrying, and
	// active work. The inspector deliberately exposes one safe aggregate.
	PaperInfoStageQueued PaperInfoStageState = "queued"
	// PaperInfoStageCompleted is inferred from the durable metadata source.
	PaperInfoStageCompleted PaperInfoStageState = "completed"
	// PaperInfoStageUnavailable means this document cannot run that stage, for
	// example extraction before a Markdown revision exists.
	PaperInfoStageUnavailable PaperInfoStageState = "unavailable"
	// PaperInfoStageUnknown means the queue backend could not be inspected.
	// It is not a persisted task failure.
	PaperInfoStageUnknown PaperInfoStageState = "unknown"
)

// PaperInfoStageStatus describes one observed paper-information stage. The
// service intentionally does not persist historical task outcomes; dead
// letters and Asynq remain the operational failure record.
type PaperInfoStageStatus struct {
	State PaperInfoStageState `json:"state"`
}

// PaperInfoStatus combines the independently observable extraction/catalog
// state with the metadata snapshot currently stored for a document.
type PaperInfoStatus struct {
	DocumentID     uuid.UUID            `json:"documentId"`
	MetadataSource PaperMetadataSource  `json:"metadataSource"`
	Extraction     PaperInfoStageStatus `json:"extraction"`
	Catalog        PaperInfoStageStatus `json:"catalog"`
}
