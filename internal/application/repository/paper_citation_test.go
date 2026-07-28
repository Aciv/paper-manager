package repository

import (
	"errors"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestNormalizedPaperCitationsDeduplicatesTargets(t *testing.T) {
	citingID := uuid.New()
	firstCitedID := uuid.New()
	secondCitedID := uuid.New()

	citations, err := normalizedPaperCitations(
		citingID,
		[]types.PaperCitation{
			{CitedDocumentID: firstCitedID, RawReference: "  First reference.  "},
			{CitedDocumentID: firstCitedID, RawReference: "Duplicate reference."},
			{CitedDocumentID: secondCitedID, RawReference: "Second reference."},
		},
	)
	if err != nil {
		t.Fatalf("normalizedPaperCitations() error = %v", err)
	}
	if len(citations) != 2 {
		t.Fatalf("normalizedPaperCitations() returned %d citations, want 2", len(citations))
	}
	if citations[0].CitedDocumentID != firstCitedID || citations[1].CitedDocumentID != secondCitedID {
		t.Fatalf("normalizedPaperCitations() did not preserve first-seen target order: %#v", citations)
	}
	if citations[0].RawReference != "  First reference.  " {
		t.Fatalf("normalizedPaperCitations() raw reference = %q, want original extraction text", citations[0].RawReference)
	}
}

func TestNormalizedPaperCitationsValidatesBeforeReplacement(t *testing.T) {
	citingID := uuid.New()

	tests := []struct {
		name      string
		citations []types.PaperCitation
		wantErr   error
	}{
		{
			name:      "missing target",
			citations: []types.PaperCitation{{RawReference: "Missing target."}},
			wantErr:   types.ErrInvalidPaperCitation,
		},
		{
			name: "self reference",
			citations: []types.PaperCitation{{
				CitedDocumentID: citingID,
				RawReference:    "Self reference.",
			}},
			wantErr: types.ErrSelfPaperCitation,
		},
		{
			name: "missing raw reference",
			citations: []types.PaperCitation{{
				CitedDocumentID: uuid.New(),
			}},
			wantErr: types.ErrInvalidPaperCitation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizedPaperCitations(citingID, test.citations)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("normalizedPaperCitations() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
