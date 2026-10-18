package service

import (
	"context"
	"sync/atomic"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// P0-1 regression: a citation whose DOI is also present on the citing paper
// must never resolve to that paper itself.
func TestP0_1ReferenceDoesNotMatchCitingDocumentByDOI(t *testing.T) {
	citingID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: citingID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/self",
	})
	papers, citationRepo := newP01PaperService(t, documents)
	referenceID := seedQueuedP01Reference(citationRepo, types.PaperReference{
		CitingDocumentID: citingID, RawReference: "A self-referential paper.",
		DOI: "10.1000/self", Title: "A self-referential paper.",
	})
	var providerCalls atomic.Int64
	service := newP01PaperInfoService(t, documents, papers, referenceID, &paperInfoProviderStub{
		name: "crossref",
		search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			providerCalls.Add(1)
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.1000/self", "A self-referential paper.", 0.95),
			}, nil
		},
	})

	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: citingID, ReferenceID: referenceID, Generation: 1,
	}); err != nil {
		t.Fatalf("ResolveMetadata() error = %v", err)
	}
	resolved := citationRepo.items[referenceID]
	if resolved.CitedDocumentID != nil {
		t.Fatalf("self DOI resolved to %s, want unresolved", *resolved.CitedDocumentID)
	}
	if resolved.ResolutionStatus != types.DocumentStageSucceeded {
		t.Fatalf("resolution status = %q, want succeeded", resolved.ResolutionStatus)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", providerCalls.Load())
	}
}

// P0-1 regression: an available local paper must win before any provider
// request, even when another citation row already exists.
func TestP0_1ReferencePrefersAvailableLocalTarget(t *testing.T) {
	citingID, targetID := uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: citingID, SourceState: types.DocumentSourceAvailable},
		types.Document{ID: targetID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/target"},
	)
	papers, citationRepo := newP01PaperService(t, documents)
	referenceID := seedQueuedP01Reference(citationRepo, types.PaperReference{
		CitingDocumentID: citingID, RawReference: "A local target.",
		DOI: "10.1000/target", Title: "A local target.",
	})
	var providerCalls atomic.Int64
	service := newP01PaperInfoService(t, documents, papers, referenceID, &paperInfoProviderStub{
		name: "crossref",
		search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			providerCalls.Add(1)
			return nil, nil
		},
	})

	if err := service.ResolveMetadata(context.Background(), types.PaperCatalogResolveCommand{
		DocumentID: citingID, ReferenceID: referenceID, Generation: 1,
	}); err != nil {
		t.Fatalf("ResolveMetadata() error = %v", err)
	}
	resolved := citationRepo.items[referenceID]
	if resolved.CitedDocumentID == nil || *resolved.CitedDocumentID != targetID {
		t.Fatalf("resolved target = %v, want %s", resolved.CitedDocumentID, targetID)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", providerCalls.Load())
	}
}

func newP01PaperService(
	t *testing.T, documents *paperDocumentRepoStub,
) (*paperService, *paperCitationRepoStub) {
	t.Helper()
	citationRepo := newPaperCitationRepoStub()
	return NewPaperService(documents, newPaperMetadataRepoStub(), citationRepo).(*paperService), citationRepo
}

func seedQueuedP01Reference(
	repo *paperCitationRepoStub, reference types.PaperReference,
) uuid.UUID {
	reference.ReferenceID = types.PaperReferenceIDForRaw(reference.CitingDocumentID, reference.RawReference)
	reference.ReferenceKey = types.PaperCitationReferenceKey(reference.RawReference)
	reference.Authors = []types.PaperAuthor{}
	reference.MetadataSource = types.PaperMetadataSourceExtraction
	reference.ResolutionStatus = types.DocumentStageQueued
	reference.ResolutionGeneration = 1
	repo.items[reference.ReferenceID] = types.PaperCitation{
		PaperReference: reference,
		Origin:         types.PaperCitationOriginExtraction,
	}
	return reference.ReferenceID
}

func newP01PaperInfoService(
	t *testing.T,
	documents interfaces.DocumentRepository,
	papers interfaces.PaperService,
	referenceID uuid.UUID,
	provider interfaces.PaperCatalogProvider,
) *paperInfoService {
	t.Helper()
	service := newPaperInfoServiceForTest(
		config.Config{PaperCatalogConcurrency: 1},
		documents,
		&paperInfoStorageStub{},
		&paperInfoExtractorStub{},
		&paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{provider}},
		papers,
		nil,
		nil,
		nil,
	).(*paperInfoService)
	if _, err := service.referenceScheduler.Register(context.Background(), types.PaperReferenceJob{
		TargetDocumentID: referenceID, Generation: 1,
	}); err != nil {
		t.Fatalf("register reference job: %v", err)
	}
	return service
}
