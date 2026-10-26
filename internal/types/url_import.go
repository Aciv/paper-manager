package types

const TypeLibraryImportURLs = "library:import_urls"

// URLImportTask describes URL acceptance/download progress. PDF processing has
// its own durable document stages after an item obtains a document ID.
type URLImportTask struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	BaseID string `json:"baseId,omitempty"`
	Total  int    `json:"total"`
	// Completed counts recorded outcomes, including stopped/unprocessed failures.
	Completed int                  `json:"completed"`
	Result    *DocumentBatchResult `json:"result,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type URLImportPayload struct {
	RequestID string                     `json:"request_id"`
	BaseID    string                     `json:"document_base_id,omitempty"`
	Commands  []DocumentURLImportCommand `json:"commands"`
}
