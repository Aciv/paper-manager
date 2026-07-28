package types

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

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
	TypePaperCatalogResolve  = "paper:catalog_resolve"  // 查询外部论文库并解析规范元数据
	//TypeKnowledgePostProcess = "knowledge:postprocess"
	//TypeManualProcess        = "knowledge:manual"
	//TypeKnowledgeListDelete  = "knowledge:list_delete"
)

// ErrInvalidPaperInfoTaskPayload indicates that a paper-information task
// cannot be safely converted into a typed application command.
var ErrInvalidPaperInfoTaskPayload = errors.New("invalid paper info task payload")

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

// PaperMetadataExtractPayload is the durable queue message for candidate
// extraction. It intentionally contains no Markdown, PDF bytes, Blocks, or
// provider response data; the worker reloads all inputs from durable storage.
type PaperMetadataExtractPayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document.
	DocumentID string `json:"document_id"`
	// MarkdownSHA256 pins the task to one parsed Markdown revision.
	MarkdownSHA256 string `json:"markdown_sha256"`
	// Attempt identifies one user-initiated paper-info run. Queue retries retain
	// the same attempt.
	Attempt int `json:"attempt,omitempty"`
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
	if err := validateSHA256("markdown_sha256", p.MarkdownSHA256); err != nil {
		return PaperMetadataExtractCommand{}, fmt.Errorf("%w: %v", ErrInvalidPaperInfoTaskPayload, err)
	}
	if p.Attempt < 0 {
		return PaperMetadataExtractCommand{}, fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperInfoTaskPayload)
	}
	return PaperMetadataExtractCommand{
		RequestID:      strings.TrimSpace(p.RequestID),
		DocumentID:     documentID,
		MarkdownSHA256: strings.ToLower(strings.TrimSpace(p.MarkdownSHA256)),
		Attempt:        p.Attempt,
	}, nil
}

// PaperCatalogResolvePayload is the durable queue message for external catalog
// lookup. Query inputs are reloaded from paper_metadata and paper_citations;
// title, authors, references, and provider responses never enter Redis.
type PaperCatalogResolvePayload struct {
	// RequestID carries HTTP correlation into asynchronous work.
	RequestID string `json:"request_id,omitempty"`
	// DocumentID identifies the source document.
	DocumentID string `json:"document_id"`
	// MarkdownSHA256 rejects stale work when lookup inputs came from parsed
	// Markdown. It is omitted for metadata-only lookup and unknown placeholders.
	MarkdownSHA256 string `json:"markdown_sha256,omitempty"`
	// Attempt identifies one user-initiated paper-info run. Queue retries retain
	// the same attempt.
	Attempt int `json:"attempt,omitempty"`
}

// Validate verifies that the catalog payload is safe to enqueue.
func (p PaperCatalogResolvePayload) Validate() error {
	_, err := p.Command()
	return err
}

// Command converts the JSON-oriented payload into an application command.
func (p PaperCatalogResolvePayload) Command() (PaperCatalogResolveCommand, error) {
	documentID, err := parsePaperTaskUUID("document_id", p.DocumentID)
	if err != nil {
		return PaperCatalogResolveCommand{}, err
	}
	if strings.TrimSpace(p.MarkdownSHA256) != "" {
		if err := validateSHA256("markdown_sha256", p.MarkdownSHA256); err != nil {
			return PaperCatalogResolveCommand{}, fmt.Errorf("%w: %v", ErrInvalidPaperInfoTaskPayload, err)
		}
	}
	if p.Attempt < 0 {
		return PaperCatalogResolveCommand{}, fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperInfoTaskPayload)
	}
	return PaperCatalogResolveCommand{
		RequestID:      strings.TrimSpace(p.RequestID),
		DocumentID:     documentID,
		MarkdownSHA256: strings.ToLower(strings.TrimSpace(p.MarkdownSHA256)),
		Attempt:        p.Attempt,
	}, nil
}

func parsePaperTaskUUID(fieldName, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a non-zero UUID", ErrInvalidPaperInfoTaskPayload, fieldName)
	}
	return id, nil
}
