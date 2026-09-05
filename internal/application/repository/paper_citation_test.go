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

func TestReconcileAutomaticCitationsRetainsResolvedTarget(t *testing.T) {
	citingID := uuid.New()
	resolvedID := uuid.New()
	newUnknownID := uuid.New()
	raw := "[1] Ada Lovelace. Notes on an analytical engine."

	items, removed := reconcileAutomaticCitations(
		[]types.PaperCitation{{
			CitingDocumentID: citingID,
			CitedDocumentID:  resolvedID,
			RawReference:     raw,
			Origin:           types.PaperCitationOriginExtraction,
		}},
		[]types.PaperCitation{{
			CitingDocumentID: citingID,
			CitedDocumentID:  newUnknownID,
			RawReference:     " [1]  ADA Lovelace. Notes on an analytical engine. ",
			Origin:           types.PaperCitationOriginExtraction,
		}},
	)
	if len(removed) != 0 {
		t.Fatalf("removed = %#v, want none", removed)
	}
	if len(items) != 1 || items[0].CitedDocumentID != resolvedID {
		t.Fatalf("items = %#v, want the prior resolved target %s", items, resolvedID)
	}
	if items[0].Origin != types.PaperCitationOriginExtraction {
		t.Fatalf("origin = %q, want extraction", items[0].Origin)
	}
}

func TestReconcileAutomaticCitationsPreservesManualAndClearsOnlyAutomatic(t *testing.T) {
	citingID := uuid.New()
	manualID := uuid.New()
	automaticID := uuid.New()
	items, removed := reconcileAutomaticCitations(
		[]types.PaperCitation{
			{
				CitingDocumentID: citingID,
				CitedDocumentID:  manualID,
				RawReference:     "Manual reference",
				Origin:           types.PaperCitationOriginManual,
			},
			{
				CitingDocumentID: citingID,
				CitedDocumentID:  automaticID,
				RawReference:     "Automatically extracted reference",
				Origin:           types.PaperCitationOriginExtraction,
			},
		},
		nil,
	)
	if len(items) != 1 || items[0].CitedDocumentID != manualID || items[0].EffectiveOrigin() != types.PaperCitationOriginManual {
		t.Fatalf("items = %#v, want only manual citation", items)
	}
	if len(removed) != 1 || removed[0].CitedDocumentID != automaticID {
		t.Fatalf("removed = %#v, want only automatic citation", removed)
	}
}

func TestReconcileAutomaticCitationsManualRawReferenceWins(t *testing.T) {
	citingID := uuid.New()
	manualID := uuid.New()
	incomingID := uuid.New()
	items, removed := reconcileAutomaticCitations(
		[]types.PaperCitation{{
			CitingDocumentID: citingID,
			CitedDocumentID:  manualID,
			RawReference:     "A manually curated reference",
			Origin:           types.PaperCitationOriginManual,
		}},
		[]types.PaperCitation{{
			CitingDocumentID: citingID,
			CitedDocumentID:  incomingID,
			RawReference:     "  a manually curated   reference ",
			Origin:           types.PaperCitationOriginExtraction,
		}},
	)
	if len(removed) != 0 || len(items) != 1 || items[0].CitedDocumentID != manualID {
		t.Fatalf("items=%#v removed=%#v, want manually curated citation only", items, removed)
	}
}
