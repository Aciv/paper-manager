package types

// Asynq queue names. MUST stay in sync with the Queues weight map in
// router.NewAsynqServer — a task enqueued to a queue that the server does not
// list will never be consumed.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
	// QueueDownload isolates bounded remote PDF traffic from parser and catalog
	// workers so a slow publisher cannot starve normal document processing.
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
	// TypePaperReferenceDispatch reads persisted bibliography entries and
	// schedules provider-owned catalog batches. It performs no provider HTTP
	// request itself.
	TypePaperReferenceDispatch = "paper:reference_dispatch"
	TypePaperCatalogResolve    = "paper:catalog_resolve" // 查询外部论文库并解析规范元数据
	TypeDocumentRemoteImport   = "document:remote_import"
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
