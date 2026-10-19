package types

// PaperReferenceProgress is a bounded citation-target projection populated
// from paper_citations, never from in-process dispatcher state.
type PaperReferenceProgress struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}
