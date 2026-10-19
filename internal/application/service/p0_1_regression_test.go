package service

import (
	"context"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// P0-1 regression: a citation whose DOI is also present on the citing paper
// must never resolve to that paper itself. The dispatcher applies the same
// exclusion before any provider request.
func TestP0_1ReferenceDoesNotMatchCitingDocumentByDOI(t *testing.T) {
	citingID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: citingID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/self",
	})
	papers, _ := newP01PaperService(t, documents)
	reference := types.PaperReference{
		CitingDocumentID: citingID, RawReference: "A self-referential paper.",
		DOI: "10.1000/self", Title: "A self-referential paper.",
	}

	match, found, err := papers.FindLocalReferenceMatch(context.Background(), reference, citingID)
	if err != nil {
		t.Fatalf("FindLocalReferenceMatch() error = %v", err)
	}
	if found {
		t.Fatalf("self DOI resolved to %s, want unresolved", match)
	}
}

// P0-1 regression: an available local paper must win before any provider
// request. The dispatcher checks this path before allocating a provider.
func TestP0_1ReferencePrefersAvailableLocalTarget(t *testing.T) {
	citingID, targetID := uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: targetID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/target"},
	)
	papers, _ := newP01PaperService(t, documents)
	reference := types.PaperReference{
		CitingDocumentID: citingID, RawReference: "A local target.",
		DOI: "10.1000/target", Title: "A local target.",
	}

	match, found, err := papers.FindLocalReferenceMatch(context.Background(), reference, citingID)
	if err != nil {
		t.Fatalf("FindLocalReferenceMatch() error = %v", err)
	}
	if !found || match != targetID {
		t.Fatalf("local target = %s, found=%t, want %s", match, found, targetID)
	}
}

func newP01PaperService(
	t *testing.T, documents *paperDocumentRepoStub,
) (*paperService, *paperCitationRepoStub) {
	t.Helper()
	citationRepo := newPaperCitationRepoStub()
	return NewPaperService(documents, newPaperMetadataRepoStub(), citationRepo).(*paperService), citationRepo
}
