package types

import (
	"time"

	"github.com/google/uuid"
)

// ProcessingRunState is the durable control-plane state of one asynchronous
// workflow. Unlike a processing span, a run remains authoritative after its
// detailed observability rows have been removed by retention.
type ProcessingRunState string

const (
	ProcessingRunQueued          ProcessingRunState = "queued"
	ProcessingRunRunning         ProcessingRunState = "running"
	ProcessingRunCancelRequested ProcessingRunState = "cancel_requested"
	ProcessingRunSucceeded       ProcessingRunState = "succeeded"
	ProcessingRunFailed          ProcessingRunState = "failed"
	ProcessingRunCancelled       ProcessingRunState = "cancelled"
)

func (state ProcessingRunState) Active() bool {
	return state == ProcessingRunQueued || state == ProcessingRunRunning || state == ProcessingRunCancelRequested
}

func (state ProcessingRunState) Terminal() bool {
	return state == ProcessingRunSucceeded || state == ProcessingRunFailed || state == ProcessingRunCancelled
}

// ProcessingWorkflowKind identifies a cancellable workflow without coupling
// the run store to a concrete queue implementation.
type ProcessingWorkflowKind string

const (
	ProcessingWorkflowDocument          ProcessingWorkflowKind = "document_processing"
	ProcessingWorkflowCatalogReference  ProcessingWorkflowKind = "catalog_reference"
	ProcessingWorkflowReferenceDownload ProcessingWorkflowKind = "reference_download"
)

// ProcessingRun is the durable execution and cancellation boundary for one
// user-visible asynchronous workflow. IdempotencyKey is stable across queue
// delivery retries; a user-requested retry must use a new key and run ID.
type ProcessingRun struct {
	ID                uuid.UUID              `gorm:"column:id;type:uuid;primaryKey" json:"id"`
	OwnerDocumentID   uuid.UUID              `gorm:"column:owner_document_id;type:uuid;not null;index" json:"ownerDocumentId"`
	WorkflowKind      ProcessingWorkflowKind `gorm:"column:workflow_kind;type:text;not null" json:"workflowKind"`
	IdempotencyKey    string                 `gorm:"column:idempotency_key;type:text;not null" json:"-"`
	Attempt           int64                  `gorm:"column:attempt;not null;default:1" json:"attempt"`
	State             ProcessingRunState     `gorm:"column:state;type:text;not null" json:"state"`
	CancelRequestedAt *time.Time             `gorm:"column:cancel_requested_at" json:"cancelRequestedAt,omitempty"`
	StartedAt         *time.Time             `gorm:"column:started_at" json:"startedAt,omitempty"`
	TerminalAt        *time.Time             `gorm:"column:terminal_at" json:"terminalAt,omitempty"`
	ErrorCode         string                 `gorm:"column:error_code;type:text;not null;default:''" json:"errorCode,omitempty"`
	ErrorReason       string                 `gorm:"column:error_reason;type:text;not null;default:''" json:"errorReason,omitempty"`
	ItemsSealedAt     *time.Time             `gorm:"column:items_sealed_at" json:"-"`
	CreatedAt         time.Time              `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt         time.Time              `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (ProcessingRun) TableName() string { return "processing_runs" }

// ProcessingRunItem is one durable unit in a fan-out workflow. Its lifecycle
// completes the owning run independently from optional observation spans.
type ProcessingRunItem struct {
	RunID       uuid.UUID          `gorm:"column:run_id;type:uuid;primaryKey"`
	ItemKey     string             `gorm:"column:item_key;type:text;primaryKey"`
	State       ProcessingRunState `gorm:"column:state;type:text;not null"`
	ErrorCode   string             `gorm:"column:error_code;type:text;not null;default:''"`
	ErrorReason string             `gorm:"column:error_reason;type:text;not null;default:''"`
	StartedAt   *time.Time         `gorm:"column:started_at"`
	TerminalAt  *time.Time         `gorm:"column:terminal_at"`
	CreatedAt   time.Time          `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time          `gorm:"column:updated_at;autoUpdateTime"`
}

func (ProcessingRunItem) TableName() string { return "processing_run_items" }
