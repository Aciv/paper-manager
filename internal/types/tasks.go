package types

// Asynq queue names. MUST stay in sync with the Queues weight map in
// router.NewAsynqServer — a task enqueued to a queue that the server does not
// list will never be consumed.
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
)

const (
	TaskScopeDocumentBase = "document_base"
	TaskScopeDocument     = "document"
	TaskScopeUnknown      = "unknown"
)

const (
	TypeDocumentProcess      = "document:process"       // 文档处理任务
	TypePaperMetadataExtract = "paper:metadata_extract" // 从 Markdown 和版面块提取论文元数据候选
	// TypePaperReferenceDispatch reads persisted bibliography entries and
	// schedules provider-owned catalog batches. It performs no provider HTTP
	// request itself.
	TypePaperReferenceDispatch = "paper:reference_dispatch"
	TypePaperCatalogResolve    = "paper:catalog_resolve" // 查询外部论文库并解析规范元数据
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
	// DocumentBaseID optionally groups documents under one document base for
	// dead-letter scope queries.
	DocumentBaseID   string   `json:"document_base_id,omitempty"`
	FilePath         string   `json:"file_path,omitempty"` // 文件路径（文件导入时使用）
	FileName         string   `json:"file_name,omitempty"` // 文件名（文件导入时使用）
	FileType         string   `json:"file_type,omitempty"` // 文件类型（文件导入时使用）
	URL              string   `json:"url,omitempty"`       // URL（URL导入时使用）
	FileURL          string   `json:"file_url,omitempty"`  // 文件资源链接（file_url导入时使用）
	Passages         []string `json:"passages,omitempty"`  // 文本段落（文本导入时使用）
	EnableMultimodel bool     `json:"enable_multimodel"`

	//EnableQuestionGeneration bool   `json:"enable_question_generation"` // 是否启用问题生成
	//QuestionCount            int    `json:"question_count,omitempty"`   // 每个chunk生成的问题数量
	//Language                 string `json:"language,omitempty"`         // Request locale for {{language}} in prompt templates
	// Attempt is the per-document attempt number this task belongs to.
	// Set on enqueue (initial parse → attempt 1; reparse → max+1) so
	// every span recorded by this task lands on the right attempt
	// branch. Asynq retries within an attempt keep the same value so
	// retried spans overwrite the previous attempt's row rather than
	// fan out into a new attempt for every retry.
	Attempt int `json:"attempt,omitempty"`
}
