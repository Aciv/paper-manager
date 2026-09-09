package types

import (
	"testing"
	"time"
)

func TestDocumentStageStatusClassification(t *testing.T) {
	for _, status := range []DocumentStageStatus{
		DocumentStageNotStarted, DocumentStageQueued, DocumentStageRunning,
		DocumentStageSucceeded, DocumentStageFailed, DocumentStageCancelled, DocumentStageSkipped,
	} {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if DocumentStageStatus("unknown").Valid() {
		t.Fatal("unknown stage status should be invalid")
	}
	if !DocumentStageQueued.Active() || !DocumentStageRunning.Active() || DocumentStageFailed.Active() {
		t.Fatal("active classification is incorrect")
	}
	if !DocumentStageSucceeded.Terminal() || !DocumentStageFailed.Terminal() ||
		!DocumentStageCancelled.Terminal() || !DocumentStageSkipped.Terminal() || DocumentStageRunning.Terminal() {
		t.Fatal("terminal classification is incorrect")
	}
}

func TestDocumentStageStateReadsIndependentFields(t *testing.T) {
	markdownTime := time.Now().UTC().Add(-time.Minute)
	sourceTime := markdownTime.Add(time.Second)
	document := Document{
		MarkdownStatus:         DocumentStageSucceeded,
		MarkdownGeneration:     2,
		MarkdownUpdatedAt:      markdownTime,
		SourceLookupStatus:     DocumentStageRunning,
		SourceLookupGeneration: 7,
		SourceLookupUpdatedAt:  sourceTime,
	}
	markdown, err := document.StageState(DocumentStageMarkdown)
	if err != nil || markdown.Status != DocumentStageSucceeded || markdown.Generation != 2 || !markdown.UpdatedAt.Equal(markdownTime) {
		t.Fatalf("markdown state = %#v, error %v", markdown, err)
	}
	source, err := document.StageState(DocumentStageSourceLookup)
	if err != nil || source.Status != DocumentStageRunning || source.Generation != 7 || !source.UpdatedAt.Equal(sourceTime) {
		t.Fatalf("source state = %#v, error %v", source, err)
	}
	if _, err := document.StageState(DocumentStage("invalid")); err == nil {
		t.Fatal("invalid stage should fail")
	}
}
