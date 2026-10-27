package types

import "testing"

func TestWorkflowReadinessAndCompletionAreIndependent(t *testing.T) {
	document := Document{
		SourceState: DocumentSourceAvailable, MarkdownStatus: DocumentStageSucceeded,
		MetadataExtractionStatus: DocumentStageSucceeded, SourceLookupStatus: DocumentStageSkipped,
		ReferenceLookupStatus: DocumentStageNotStarted, SourceLookupMode: SourceLookupSkip,
	}
	summary := document.WorkflowSummary()
	if !summary.ReadingReady || summary.WorkflowStatus != "waiting" || summary.SourceLookup != SourceLookupSkip {
		t.Fatalf("manual reference wait = %#v", summary)
	}
	document.SourceLookupStatus = DocumentStageRunning
	summary = document.WorkflowSummary()
	if !summary.ReadingReady || summary.WorkflowStatus != "processing" {
		t.Fatalf("manual source lookup blocked reading = %#v", summary)
	}
	for _, target := range summary.RetryTargets {
		if target == "catalog" {
			t.Fatal("running source lookup advertised a retry")
		}
	}
	document.SourceLookupStatus, document.ReferenceLookupStatus = DocumentStageSkipped, DocumentStageSucceeded
	if summary := document.WorkflowSummary(); summary.WorkflowStatus != "succeeded" || !summary.ReadingReady {
		t.Fatalf("completed = %#v", summary)
	}
	document.SourceLookupStatus = DocumentStageFailed
	if summary := document.WorkflowSummary(); summary.WorkflowStatus != "partial" || !summary.ReadingReady {
		t.Fatalf("catalog failure = %#v", summary)
	}
	document.MarkdownStatus = DocumentStageSkipped
	if summary := document.WorkflowSummary(); summary.ReadingReady {
		t.Fatal("skipped parsing was reported as ready")
	}
	document.SourceState = DocumentSourceDeleting
	if summary := document.WorkflowSummary(); summary.ReadingReady || len(summary.RetryTargets) != 0 {
		t.Fatalf("deleting document = %#v", summary)
	}
}

func TestBatchImportSummaryIncludesMetadataFailure(t *testing.T) {
	result := DocumentBatchResult{Items: []DocumentBatchItemResult{
		{Status: DocumentImportItemAccepted},
		{Status: DocumentImportItemDuplicate, ImportMetadata: &DocumentImportMetadataResult{Status: "failed"}},
		{Status: DocumentImportItemRejected},
	}}
	result.UpdateSummary()
	if result.Status != "partial" || result.Summary.Succeeded != 1 ||
		result.Summary.Partial != 1 || result.Summary.Failed != 1 {
		t.Fatalf("mixed batch = %#v", result)
	}
	result.Items = []DocumentBatchItemResult{{Status: DocumentImportItemFailed}}
	result.UpdateSummary()
	if result.Status != "failed" {
		t.Fatal("all failures reported success")
	}
	result.Items = []DocumentBatchItemResult{{Status: DocumentImportItemDuplicate}}
	result.UpdateSummary()
	if result.Status != "succeeded" {
		t.Fatal("valid duplicate reported failure")
	}
}
