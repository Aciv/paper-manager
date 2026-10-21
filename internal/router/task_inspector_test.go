package router

import (
	"testing"

	"paperMaster/internal/types"

	"github.com/hibiken/asynq"
)

func TestTaskInspectorAdaptersShareOneAsynqInspector(t *testing.T) {
	backend := &asynq.Inspector{}
	shared := &SharedAsynqInspector{inspector: backend}
	documentInspector, ok := NewTaskInspector(shared).(*asynqTaskInspector)
	if !ok || documentInspector.inspector != backend {
		t.Fatalf("document inspector did not reuse shared backend: %#v", documentInspector)
	}
	resultInspector, ok := NewTaskResultInspector(shared).(*SharedAsynqInspector)
	if !ok || resultInspector != shared || resultInspector.inspector != backend {
		t.Fatalf("result inspector did not reuse shared backend: %#v", resultInspector)
	}
}

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

func TestRecordSnapshotTaskCapturesStageAndReferenceEnvelopes(t *testing.T) {
	snapshot := types.NewQueuedWorkSnapshot()
	recordSnapshotTask(&snapshot, types.TypeDocumentRecognize,
		[]byte(`{"document_id":"doc-1","stage":"markdown","generation":2}`))
	recordSnapshotTask(&snapshot, types.TypePaperQueryApply,
		[]byte(`{"kind":"source","document_id":"doc-2","stage":"source_lookup","generation":3}`))
	recordSnapshotTask(&snapshot, types.TypePaperQueryApply,
		[]byte(`{"kind":"reference","reference_id":"ref-1","generation":4}`))
	recordSnapshotTask(&snapshot, types.TypePaperQueryApply,
		[]byte(`{"kind":"reference","reference_id":"ref-2","generation":0}`))
	recordSnapshotTask(&snapshot, "unrelated:task",
		[]byte(`{"document_id":"doc-3","stage":"markdown","generation":1}`))

	if !snapshot.HasStage("doc-1", types.DocumentStageMarkdown, 2) {
		t.Fatal("document stage task was not captured")
	}
	if !snapshot.HasStage("doc-2", types.DocumentStageSourceLookup, 3) {
		t.Fatal("source query-apply task was not captured")
	}
	if !snapshot.HasReference("ref-1", 4) {
		t.Fatal("reference query-apply task was not captured")
	}
	if snapshot.HasReference("ref-2", 0) {
		t.Fatal("zero-generation reference task was captured")
	}
	if snapshot.HasStage("doc-3", types.DocumentStageMarkdown, 1) {
		t.Fatal("unrelated task was captured")
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
