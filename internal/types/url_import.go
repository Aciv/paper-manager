package types

const TypeLibraryImportURLs = "library:import_urls"

// URLImportTask describes URL acceptance/download progress. PDF processing has
// its own durable document stages after an item obtains a document ID.
type URLImportTask struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	BaseID  string   `json:"baseId,omitempty"`
	BaseIDs []string `json:"baseIds,omitempty"`
	Total   int      `json:"total"`
	// Completed counts recorded outcomes, including stopped/unprocessed failures.
	Completed int                        `json:"completed"`
	Summary   URLImportSummary           `json:"summary"`
	RetryURLs []DocumentURLImportCommand `json:"retryUrls,omitempty"`
	Result    *DocumentBatchResult       `json:"result,omitempty"`
	Error     string                     `json:"error,omitempty"`
}

type URLImportSummary struct {
	MetadataFailed int `json:"metadataFailed"`
	Accepted       int `json:"accepted"`
	Duplicate      int `json:"duplicate"`
	Rejected       int `json:"rejected"`
	Failed         int `json:"failed"`
}

type URLImportPayload struct {
	RequestID string                     `json:"request_id"`
	BaseID    string                     `json:"document_base_id,omitempty"`
	BaseIDs   []string                   `json:"document_base_ids,omitempty"`
	Commands  []DocumentURLImportCommand `json:"commands"`
}
