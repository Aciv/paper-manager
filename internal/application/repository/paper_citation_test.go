package repository

import (
	"errors"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func uuidPtr(value uuid.UUID) *uuid.UUID { return &value }

func TestNormalizedPaperCitationsDeduplicatesWithinOnePaper(t *testing.T) {
	citingID := uuid.New()
	firstCitedID := uuid.New()
	firstRaw := "  First reference.  "
	secondRaw := "Second reference."

	citations, err := normalizedPaperCitations(
		citingID,
		[]types.PaperCitation{
			{PaperReference: types.PaperReference{ReferenceID: uuid.New(), CitedDocumentID: uuidPtr(firstCitedID), RawReference: firstRaw}},
			{PaperReference: types.PaperReference{ReferenceID: uuid.New(), RawReference: " First   reference. "}},
			{PaperReference: types.PaperReference{ReferenceID: uuid.New(), RawReference: secondRaw}},
		},
	)
	if err != nil {
		t.Fatalf("normalizedPaperCitations() error = %v", err)
	}
	if len(citations) != 2 {
		t.Fatalf("normalizedPaperCitations() returned %d citations, want 2", len(citations))
	}
	if citations[0].RawReference != firstRaw || citations[1].RawReference != secondRaw {
		t.Fatalf("normalizedPaperCitations() did not preserve first-seen order: %#v", citations)
	}
	for _, citation := range citations {
		if citation.CitingDocumentID != citingID || citation.ReferenceKey == "" {
			t.Fatalf("normalized citation = %#v", citation)
		}
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
			name: "self reference",
			citations: []types.PaperCitation{{
				PaperReference: types.PaperReference{
					ReferenceID:     uuid.New(),
					CitedDocumentID: uuidPtr(citingID),
					RawReference:    "Self reference.",
				},
			}},
			wantErr: types.ErrSelfPaperCitation,
		},
		{
			name: "missing raw reference",
			citations: []types.PaperCitation{{
				PaperReference: types.PaperReference{ReferenceID: uuid.New()},
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

func TestNormalizedAutomaticPaperCitationsDropsSelfTarget(t *testing.T) {
	citingID := uuid.New()
	citations, err := normalizedAutomaticPaperCitations(citingID, []types.PaperCitation{{
		PaperReference: types.PaperReference{
			ReferenceID:     uuid.New(),
			CitedDocumentID: uuidPtr(citingID),
			RawReference:    "Self reference.",
		},
	}})
	if err != nil {
		t.Fatalf("normalizedAutomaticPaperCitations() error = %v", err)
	}
	if len(citations) != 1 || citations[0].CitedDocumentID != nil ||
		citations[0].ResolutionStatus != types.DocumentStageNotStarted {
		t.Fatalf("self target was not dropped: %#v", citations)
	}
}
