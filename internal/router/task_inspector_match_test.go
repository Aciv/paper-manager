package router

import (
	"encoding/json"
	"testing"

	"paperMaster/internal/types"
)

func TestMatchesDocumentStage(t *testing.T) {
	payload, err := json.Marshal(DocumentIDProbe{DocumentID: "doc-a", Stage: types.DocumentStageMarkdown, Generation: 4})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		taskType   string
		documentID string
		stage      types.DocumentStage
		generation int64
		want       bool
	}{
		{"exact", types.TypeDocumentRecognize, "doc-a", types.DocumentStageMarkdown, 4, true},
		{"new generation", types.TypeDocumentRecognize, "doc-a", types.DocumentStageMarkdown, 5, false},
		{"other stage", types.TypeDocumentRecognize, "doc-a", types.DocumentStageMetadataExtraction, 4, false},
		{"other document", types.TypeDocumentRecognize, "doc-b", types.DocumentStageMarkdown, 4, false},
		{"unsupported task", "other", "doc-a", types.DocumentStageMarkdown, 4, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesDocumentStage(tc.taskType, payload, tc.documentID, tc.stage, tc.generation); got != tc.want {
				t.Fatalf("matchesDocumentStage() = %t, want %t", got, tc.want)
			}
		})
	}
}
