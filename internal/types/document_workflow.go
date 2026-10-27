package types

// DocumentWorkflowSummary is computed from durable stage columns, never spans.
// ReadingReady permits notes work while unrelated catalog work is still active.
type DocumentWorkflowSummary struct {
	ReadingReady   bool             `json:"readingReady"`
	WorkflowStatus string           `json:"workflowStatus"`
	SourceLookup   SourceLookupMode `json:"sourceLookupMode"`
	RetryTargets   []string         `json:"retryTargets"`
}

func (document Document) WorkflowSummary() DocumentWorkflowSummary {
	mode, _ := document.SourceLookupMode.Normalized()
	summary := DocumentWorkflowSummary{
		ReadingReady: document.SourceState == DocumentSourceAvailable &&
			document.MarkdownStatus == DocumentStageSucceeded,
		WorkflowStatus: "waiting", SourceLookup: mode, RetryTargets: []string{},
	}
	if document.SourceState != DocumentSourceAvailable {
		summary.WorkflowStatus = "failed"
		return summary
	}
	active, failed, waiting, succeeded := false, false, false, false
	for _, stage := range []DocumentStage{
		DocumentStageMarkdown, DocumentStageMetadataExtraction, DocumentStageSourceLookup, DocumentStageReferenceLookup,
	} {
		state, _ := document.StageState(stage)
		active = active || state.Status.Active()
		failed = failed || state.Status == DocumentStageFailed || state.Status == DocumentStageCancelled
		waiting = waiting || state.Status == DocumentStageNotStarted || state.Status == ""
		succeeded = succeeded || state.Status == DocumentStageSucceeded
		if state.Status.Active() {
			continue
		}
		switch stage {
		case DocumentStageMarkdown:
			if state.Status == DocumentStageFailed || state.Status == DocumentStageCancelled {
				summary.RetryTargets = append(summary.RetryTargets, "markdown")
			}
		case DocumentStageMetadataExtraction:
			if summary.ReadingReady {
				summary.RetryTargets = append(summary.RetryTargets, "extraction")
			}
		case DocumentStageSourceLookup:
			if summary.ReadingReady || document.DOI != "" {
				summary.RetryTargets = append(summary.RetryTargets, "catalog")
			}
		case DocumentStageReferenceLookup:
			if summary.ReadingReady && document.MetadataExtractionStatus == DocumentStageSucceeded {
				summary.RetryTargets = append(summary.RetryTargets, "reference")
			}
		}
	}
	switch {
	case active:
		summary.WorkflowStatus = "processing"
	case failed && succeeded:
		summary.WorkflowStatus = "partial"
	case failed:
		summary.WorkflowStatus = "failed"
	case waiting || !summary.ReadingReady:
		summary.WorkflowStatus = "waiting"
	default:
		summary.WorkflowStatus = "succeeded"
	}
	return summary
}
