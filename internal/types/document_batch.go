package types

// MaxDocumentBatchItems bounds a request or queued URL import. Processing is
// still asynchronous per document, but bounded acceptance protects the API,
// storage, and queue from an unbounded browser submission.
const MaxDocumentBatchItems = 20

// DocumentURLImportBatchRequest accepts independently configurable remote PDF
// imports. Each item uses the same validation and SSRF-protected path as the
// existing single URL import endpoint.
type DocumentURLImportBatchRequest struct {
	URLs []DocumentURLImportCommand `json:"urls"`
}

// DocumentImportItemStatus describes the outcome of accepting one input.
// HTTP batches do not create database rows. Queued URL imports retain partial
// results in Asynq; document stages remain the source of PDF processing state.
type DocumentImportItemStatus string

const (
	DocumentImportItemAccepted  DocumentImportItemStatus = "accepted"
	DocumentImportItemDuplicate DocumentImportItemStatus = "duplicate"
	DocumentImportItemRejected  DocumentImportItemStatus = "rejected"
	DocumentImportItemFailed    DocumentImportItemStatus = "failed"
)

// DocumentBatchItemResult is safe for HTTP responses. Error is a stable,
// client-safe category rather than a wrapped storage, database, or network
// error. ProcessingQueued and Retryable are present only for newly created
// documents, including the recoverable queue-outage case.
type DocumentBatchItemResult struct {
	Index            int                      `json:"index"`
	Filename         string                   `json:"filename,omitempty"`
	URL              string                   `json:"url,omitempty"`
	Status           DocumentImportItemStatus `json:"status"`
	Document         *Document                `json:"document,omitempty"`
	ProcessingQueued *bool                    `json:"processingQueued,omitempty"`
	Retryable        *bool                    `json:"retryable,omitempty"`
	Error            string                   `json:"error,omitempty"`

	ImportMetadata *DocumentImportMetadataResult `json:"importMetadata,omitempty"`
}

// DocumentBatchResult aggregates independently accepted items. Callers must
// inspect every item: a successful HTTP response deliberately permits partial
// success.
type DocumentBatchResult struct {
	Status  string                    `json:"status"`
	Summary DocumentBatchSummary      `json:"summary"`
	Items   []DocumentBatchItemResult `json:"items"`
	Stopped bool                      `json:"stopped,omitempty"`
	Error   string                    `json:"error,omitempty"`
}

type DocumentBatchSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Partial   int `json:"partial"`
	Failed    int `json:"failed"`
}

// UpdateSummary distinguishes source acceptance from metadata or queue failure.
// It never claims that accepted documents have finished their PDF processing.
func (result *DocumentBatchResult) UpdateSummary() {
	summary := DocumentBatchSummary{Total: len(result.Items)}
	for _, item := range result.Items {
		accepted := item.Status == DocumentImportItemAccepted || item.Status == DocumentImportItemDuplicate
		if !accepted {
			summary.Failed++
		} else if (item.ImportMetadata != nil && item.ImportMetadata.Status == "failed") ||
			(item.ProcessingQueued != nil && !*item.ProcessingQueued) {
			summary.Partial++
		} else {
			summary.Succeeded++
		}
	}
	result.Summary = summary
	result.Status = "succeeded"
	if summary.Failed == summary.Total && summary.Total > 0 {
		result.Status = "failed"
	} else if summary.Failed > 0 || summary.Partial > 0 || result.Stopped {
		result.Status = "partial"
	}
}
