package router

import "testing"

func TestMatchesDocumentUsesDocumentIDPayloadField(t *testing.T) {
	const documentID = "39a2d54f-412a-4c6c-84e1-9a9ea5034c6d"
	if !matchesDocument("document:process", []byte(`{"document_id":"`+documentID+`"}`), documentID) {
		t.Fatal("document task with matching document_id was not selected")
	}
	if matchesDocument("document:process", []byte(`{"knowledge_id":"`+documentID+`"}`), documentID) {
		t.Fatal("legacy knowledge_id payload was unexpectedly selected")
	}
	if matchesDocument("unrelated:task", []byte(`{"document_id":"`+documentID+`"}`), documentID) {
		t.Fatal("unrelated task type was unexpectedly selected")
	}
}
