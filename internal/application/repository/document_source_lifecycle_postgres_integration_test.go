package repository

import (
	"context"
	"sync"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDocumentCanonicalizationMovesCitationsPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	citations := NewPaperCitationRepository(db)

	canonicalID, sourceOne, sourceTwo := uuid.New(), uuid.New(), uuid.New()
	loserOne, loserTwo := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{canonicalID, sourceOne, sourceTwo, loserOne, loserTwo} {
		if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(id, "source.pdf", "canonical/"+id.String(), "")); err != nil {
			t.Fatalf("create document %s: %v", id, err)
		}
	}
	first := testResolvedCitation(sourceOne, "Shared reference one", loserOne)
	second := testResolvedCitation(sourceTwo, "Shared reference two", loserTwo)
	if _, err := citations.AddCitation(ctx, first); err != nil {
		t.Fatalf("add first citation: %v", err)
	}
	if _, err := citations.AddCitation(ctx, second); err != nil {
		t.Fatalf("add second citation: %v", err)
	}
	// The loser itself cites the canonical document. The merge must drop this
	// row before rewriting citing_document_id, otherwise the self-citation
	// CHECK would block the move.
	loserOutgoing := testResolvedCitation(loserOne, "Loser outgoing reference", canonicalID)
	loserOutgoing.Origin = types.PaperCitationOriginExtraction
	if err := db.Create(&loserOutgoing).Error; err != nil {
		t.Fatalf("add loser outgoing citation: %v", err)
	}

	gate := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, loserID := range []uuid.UUID{loserOne, loserTwo} {
		wait.Add(1)
		go func(id uuid.UUID) {
			defer wait.Done()
			<-gate
			errs <- documents.MergeIntoCanonical(ctx, id, canonicalID)
		}(loserID)
	}
	close(gate)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent MergeIntoCanonical: %v", err)
		}
	}

	for _, source := range []uuid.UUID{sourceOne, sourceTwo} {
		items, err := citations.ListByCitingDocumentID(ctx, source)
		if err != nil {
			t.Fatalf("list citations: %v", err)
		}
		if len(items) != 1 || items[0].CitedDocumentID == nil || *items[0].CitedDocumentID != canonicalID {
			t.Fatalf("citation target = %#v, want canonical %s", items, canonicalID)
		}
	}
	for _, loserID := range []uuid.UUID{loserOne, loserTwo} {
		loser, err := documents.Get(ctx, loserID)
		if err != nil {
			t.Fatalf("load loser: %v", err)
		}
		if loser.SourceState != types.DocumentSourceMerged ||
			loser.CanonicalDocumentID == nil || *loser.CanonicalDocumentID != canonicalID {
			t.Fatalf("loser lifecycle = %#v", loser)
		}
	}
	var selfCitations int64
	if err := db.Table("paper_citations").
		Where("citing_document_id = cited_document_id").Count(&selfCitations).Error; err != nil {
		t.Fatalf("count self citations: %v", err)
	}
	if selfCitations != 0 {
		t.Fatalf("self citations after merge = %d, want 0", selfCitations)
	}
	// P0-2: deleting a canonical paper must cascade merged tombstones instead of
	// being blocked by the self-referencing foreign key.
	if err := documents.Delete(ctx, canonicalID); err != nil {
		t.Fatalf("delete canonical: %v", err)
	}
	for _, loserID := range []uuid.UUID{loserOne, loserTwo} {
		if _, err := documents.Get(ctx, loserID); err == nil {
			t.Fatalf("merged child %s still exists after canonical deletion", loserID)
		}
	}
}

func TestReferenceProgressIsDisjointPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)

	sourceID := uuid.New()
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(sourceID, "source.pdf", "progress/source", "")); err != nil {
		t.Fatal(err)
	}
	statuses := []types.DocumentStageStatus{
		types.DocumentStageQueued, types.DocumentStageRunning, types.DocumentStageSucceeded,
		types.DocumentStageFailed, types.DocumentStageCancelled,
	}
	for index, status := range statuses {
		citation := testResolvedCitation(sourceID, "Progress reference "+string(rune('a'+index)), uuid.Nil)
		citation.ResolutionStatus = status
		if err := db.Create(&citation).Error; err != nil {
			t.Fatal(err)
		}
	}
	progress, err := documents.CitationTargetProgress(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Queued != 1 || progress.Running != 1 || progress.Completed != 1 ||
		progress.Failed != 1 || progress.Cancelled != 1 {
		t.Fatalf("reference progress = %#v", progress)
	}
}

func testResolvedCitation(citingID uuid.UUID, raw string, citedDocumentID uuid.UUID) types.PaperCitation {
	citation := types.PaperCitation{
		PaperReference: types.PaperReference{
			ReferenceID:      types.PaperReferenceIDForRaw(citingID, raw),
			CitingDocumentID: citingID,
			ReferenceKey:     types.PaperCitationReferenceKey(raw),
			RawReference:     raw,
			ResolutionStatus: types.DocumentStageNotStarted,
			MetadataSource:   types.PaperMetadataSourceExtraction,
			Authors:          []types.PaperAuthor{},
		},
		Origin: types.PaperCitationOriginExtraction,
	}
	if citedDocumentID != uuid.Nil {
		citation.CitedDocumentID = &citedDocumentID
		citation.ResolutionStatus = types.DocumentStageSucceeded
	}
	return citation
}

func sourceDocumentForCanonicalTest(id uuid.UUID, filename, storageKey, hash string) types.Document {
	return types.Document{
		ID: id, OriginalFilename: filename, ContentType: "application/pdf", SizeBytes: 1,
		StorageKey: storageKey, FileSHA256: hash,
		SourceState: types.DocumentSourceAvailable,
	}
}
