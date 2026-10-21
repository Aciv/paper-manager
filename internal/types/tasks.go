package types

import "errors"

var (
	ErrTaskNotFound             = errors.New("task not found")
	ErrTaskInspectorUnavailable = errors.New("task inspector unavailable")
)

// Asynq queue names. MUST stay in sync with the Queues weight map in
// router.NewAsynqServer — a task enqueued to a queue that the server does not
// list will never be consumed.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
	// QueueDownload is reserved for explicit user-requested reference PDF downloads.
	QueueDownload = "download"
)

const (
	TaskScopeDocumentBase = "document_base"
	TaskScopeDocument     = "document"
	TaskScopeUnknown      = "unknown"
)

const (
	// TypeDocumentRecognize performs only file recognition and persists the
	// resulting Markdown/Blocks revision.
	TypeDocumentRecognize    = "document:recognize"
	TypePaperMetadataExtract = "paper:metadata_extract" // 从 Markdown 和版面块提取论文元数据候选
	// TypePaperReferenceQueryEnqueue reads persisted bibliography entries and
	// submits them to the in-process catalog dispatcher. It performs no
	// provider HTTP request itself.
	TypePaperReferenceQueryEnqueue = "paper:reference_query_enqueue"
	// TypePaperSourceQueryEnqueue starts one source-paper catalog lookup and hands
	// it to the in-process dispatcher; it does not query providers itself.
	TypePaperSourceQueryEnqueue = "paper:source_query_enqueue"
	// TypePaperQueryApply applies one dispatcher result and finalizes the
	// corresponding source stage or reference resolution.
	TypePaperQueryApply      = "paper:query_apply"
	TypeDocumentRemoteImport = "document:remote_import"
	TypeBibliographyGenerate = "bibliography:generate"
	//TypeKnowledgePostProcess = "knowledge:postprocess"
	//TypeManualProcess        = "knowledge:manual"
	//TypeKnowledgeListDelete  = "knowledge:list_delete"
)

type DocumentProcessPayload struct {
	// RequestID carries the HTTP correlation ID into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the document being parsed. It is also the key used
	// by cancellation and dead-letter handling.
	DocumentID string `json:"document_id"`
	// Stage and Generation are the durable execution fence.
	Stage      DocumentStage `json:"stage,omitempty"`
	Generation int64         `json:"generation,omitempty"`
}

type TaskRecord struct {
	ID      string
	Type    string
	State   string
	Payload []byte
	Result  []byte
}
