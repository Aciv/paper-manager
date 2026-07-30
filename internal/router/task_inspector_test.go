package router

import "testing"

func TestMatchesDocumentUsesDocumentIDPayloadField(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	if !matchesDocument("document:process", []byte(`{"document_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("document task with matching document_id was not selected")
	}
	if matchesDocument("document:process", []byte(`{"knowledge_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("legacy knowledge_id payload was unexpectedly selected")
	}
	if matchesDocument("unrelated:task", []byte(`{"document_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("unrelated task type was unexpectedly selected")
	}
	if !matchesDocument(
		"paper:metadata_extract",
		[]byte(`{"document_id":"`+documentID+`"}`),
		documentID,
		"paper:metadata_extract",
	) {
		t.Fatal("paper extraction task was not selected")
	}
	if matchesDocument(
		"paper:catalog_resolve",
		[]byte(`{"document_id":"`+documentID+`"}`),
		documentID,
		"paper:metadata_extract",
	) {
		t.Fatal("task-type-specific probe selected the wrong paper task")
	}
}

func TestMatchesReferenceRunScopesCancellationToOneRun(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	const runID = "6e7db7c8-ee58-4ca9-9974-42db08b06b99"
	payload := []byte(`{"document_id":"` + documentID + `","reference_run_id":"` + runID + `"}`)
	if !matchesReferenceRun("paper:catalog_resolve", payload, documentID, runID) {
		t.Fatal("reference batch with matching run id was not selected")
	}
	if !matchesReferenceRun("paper:reference_dispatch", payload, documentID, runID) {
		t.Fatal("reference dispatcher with matching run id was not selected")
	}
	if matchesReferenceRun("paper:catalog_resolve", payload, documentID, "other-run") {
		t.Fatal("reference batch for a different run was selected")
	}
	if matchesReferenceRun("paper:metadata_extract", payload, documentID, runID) {
		t.Fatal("metadata extraction was selected for reference-run cancellation")
	}
}
