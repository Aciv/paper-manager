package types

// DocumentProjectionQuery requests a bounded batch read model for the library
// first paint and the citation-progress dialog. The frontend pages documents
// with the normal list endpoints, then asks for all visible rows in batches
// instead of issuing one request per document.
type DocumentProjectionQuery struct {
	DocumentIDs       []string `json:"documentIds"`
	IncludeMetadata   bool     `json:"includeMetadata"`
	IncludeProcessing bool     `json:"includeProcessing"`
}

// DocumentProjection is one row of the batch read model. Optional sections are
// omitted when the caller did not request them.
type DocumentProjection struct {
	Document   Document                `json:"document"`
	Metadata   *PaperMetadata          `json:"metadata,omitempty"`
	Processing *DocumentProcessingView `json:"processing,omitempty"`
}
