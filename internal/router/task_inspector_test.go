package router

import (
	"testing"

	"paperMaster/internal/types"
)

func TestMatchesDocumentUsesDocumentIDPayloadField(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	if !matchesDocument(types.TypeDocumentRecognize, []byte(`{"document_id":"`+documentID+`"}`), documentID, "") {
		t.Fatal("document task with matching document_id was not selected")
	}
	if matchesDocument(types.TypeDocumentRecognize, []byte(`{"knowledge_id":"`+documentID+`"}`), documentID, "") {
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
		"paper:source_query_enqueue",
		[]byte(`{"document_id":"`+documentID+`"}`),
		documentID,
		"paper:metadata_extract",
	) {
		t.Fatal("task-type-specific probe selected the wrong paper task")
	}
}

func TestMatchesDocumentStageMatchesSourceQueryApplyResult(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	payload := []byte(`{"kind":"source","document_id":"` + documentID + `","stage":"source_lookup","generation":3}`)
	if !matchesDocumentStage(types.TypePaperQueryApply, payload, documentID, types.DocumentStageSourceLookup, 3) {
		t.Fatal("source query-apply result was not matched to its stage")
	}
	if matchesDocumentStage(types.TypePaperQueryApply, payload, documentID, types.DocumentStageReferenceLookup, 3) {
		t.Fatal("source result matched the wrong stage")
	}
	referencePayload := []byte(`{"kind":"reference","document_id":"` + documentID + `","reference_id":"ref","stage":"reference_lookup","generation":3}`)
	if matchesDocumentStage(types.TypePaperQueryApply, referencePayload, documentID, types.DocumentStageReferenceLookup, 3) {
		t.Fatal("reference result must not count as a source_lookup task")
	}
}
