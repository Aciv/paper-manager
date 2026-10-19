package repository

import (
	"context"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestMergeAutomaticCitationsDiffsRowsPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	repo := NewPaperCitationRepository(db)

	citingID := uuid.New()
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(
		citingID, "citing.pdf", "merge/citing", "",
	)); err != nil {
		t.Fatalf("create citing document: %v", err)
	}
	referenceA := testResolvedCitation(citingID, "Reference A", uuid.Nil)
	referenceB := testResolvedCitation(citingID, "Reference B", uuid.Nil)
	first, err := repo.MergeAutomaticCitations(ctx, citingID, []types.PaperCitation{referenceA, referenceB})
	if err != nil || len(first) != 2 {
		t.Fatalf("first merge = %#v, err=%v", first, err)
	}
	byRaw := citationsByRawForTest(first)
	idA := byRaw["Reference A"].ReferenceID

	queued, applied, err := repo.QueueResolution(ctx, idA)
	if err != nil || !applied {
		t.Fatalf("queue reference A: applied=%v err=%v", applied, err)
	}

	referenceA.PaperReference = queued
	referenceA.PaperReference.Title = "Reference A revised"
	referenceC := testResolvedCitation(citingID, "Reference C", uuid.Nil)
	second, err := repo.MergeAutomaticCitations(ctx, citingID, []types.PaperCitation{referenceA, referenceC})
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second merge rows = %#v, want A and C", second)
	}
	byRaw = citationsByRawForTest(second)
	if _, exists := byRaw["Reference B"]; exists {
		t.Fatalf("removed reference B is still present: %#v", second)
	}
	gotA := byRaw["Reference A"]
	if gotA.ReferenceID != idA {
		t.Fatalf("reference A id = %s, want stable %s", gotA.ReferenceID, idA)
	}
	if gotA.Title != "Reference A revised" {
		t.Fatalf("reference A title = %q, want updated title", gotA.Title)
	}
	if gotA.ResolutionStatus != types.DocumentStageQueued || gotA.ResolutionGeneration != queued.ResolutionGeneration {
		t.Fatalf("reference A resolution = %s/%d, want preserved queued/%d",
			gotA.ResolutionStatus, gotA.ResolutionGeneration, queued.ResolutionGeneration)
	}
	if _, exists := byRaw["Reference C"]; !exists {
		t.Fatalf("new reference C missing: %#v", second)
	}

	manual, err := repo.AddCitation(ctx, types.PaperCitation{
		PaperReference: types.PaperReference{
			ReferenceID:      types.PaperReferenceIDForRaw(citingID, "Manual reference"),
			CitingDocumentID: citingID,
			RawReference:     "Manual reference", Title: "Manual title",
		},
	})
	if err != nil {
		t.Fatalf("create manual citation: %v", err)
	}
	if _, err := repo.MergeAutomaticCitations(ctx, citingID, []types.PaperCitation{
		testResolvedCitation(citingID, "Manual reference", uuid.Nil),
	}); err != nil {
		t.Fatalf("merge over manual citation: %v", err)
	}
	all, err := repo.ListByCitingDocumentID(ctx, citingID)
	if err != nil {
		t.Fatalf("reload citations: %v", err)
	}
	var stored types.PaperCitation
	for _, candidate := range all {
		if candidate.ReferenceID == manual.ReferenceID {
			stored = candidate
			break
		}
	}
	if stored.Origin != types.PaperCitationOriginManual || stored.Title != "Manual title" {
		t.Fatalf("manual citation was overwritten: %#v", stored)
	}
}

func citationsByRawForTest(citations []types.PaperCitation) map[string]types.PaperCitation {
	result := make(map[string]types.PaperCitation, len(citations))
	for _, citation := range citations {
		result[citation.RawReference] = citation
	}
	return result
}

func TestPaperCitationResolutionStateMachinePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	repo := NewPaperCitationRepository(db)

	citingID, targetID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{citingID, targetID} {
		if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(id, "source.pdf", "resolution/"+id.String(), "")); err != nil {
			t.Fatalf("create document: %v", err)
		}
	}
	citation := testResolvedCitation(citingID, "Resolution target", uuid.Nil)
	saved, err := repo.AddCitation(ctx, citation)
	if err != nil {
		t.Fatalf("seed citation: %v", err)
	}
	queued, applied, err := repo.QueueResolution(ctx, saved.ReferenceID)
	if err != nil || !applied || queued.ResolutionStatus != types.DocumentStageQueued || queued.ResolutionGeneration != 1 {
		t.Fatalf("queue = %#v, applied=%v, err=%v", queued, applied, err)
	}
	running, applied, err := repo.StartResolution(ctx, saved.ReferenceID, queued.ResolutionGeneration)
	if err != nil || !applied || running.ResolutionStatus != types.DocumentStageRunning {
		t.Fatalf("start = %#v, applied=%v, err=%v", running, applied, err)
	}
	title := "Catalog title"
	source := types.PaperMetadataSourceCatalog
	finished, applied, err := repo.FinishResolution(
		ctx, saved.ReferenceID, running.ResolutionGeneration, types.DocumentStageSucceeded,
		"arxiv", "", "", &types.PaperReferenceMetadataPatch{Title: &title, MetadataSource: &source}, &targetID,
	)
	if err != nil || !applied || finished.CitedDocumentID == nil || *finished.CitedDocumentID != targetID ||
		finished.Title != title || finished.ResolutionStatus != types.DocumentStageSucceeded {
		t.Fatalf("finish = %#v, applied=%v, err=%v", finished, applied, err)
	}
	if err := documents.Delete(ctx, targetID); err != nil {
		t.Fatalf("delete target: %v", err)
	}
	reset, err := repo.GetReference(ctx, saved.ReferenceID)
	if err != nil {
		t.Fatalf("reload reference: %v", err)
	}
	if reset.CitedDocumentID != nil || reset.ResolutionStatus != types.DocumentStageNotStarted ||
		reset.ResolutionGeneration != finished.ResolutionGeneration+1 {
		t.Fatalf("reset reference = %#v", reset)
	}
}

func TestPaperCitationDownloadStatePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	repo := NewPaperCitationRepository(db)

	citingID, targetID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{citingID, targetID} {
		if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(id, "source.pdf", "download/"+id.String(), "")); err != nil {
			t.Fatalf("create document: %v", err)
		}
	}
	saved, err := repo.AddCitation(ctx, testResolvedCitation(citingID, "Download target", uuid.Nil))
	if err != nil {
		t.Fatalf("seed citation: %v", err)
	}

	first := uuid.New()
	queued, applied, err := repo.QueueDownload(ctx, saved.ReferenceID, first)
	if err != nil || !applied || queued.DownloadAttempt == nil || *queued.DownloadAttempt != first ||
		queued.DownloadRequestedAt == nil || queued.DownloadErrorMessage != nil {
		t.Fatalf("first queue = %#v, applied=%v, err=%v", queued, applied, err)
	}
	stale := uuid.New()
	if applied, err := repo.FailDownload(ctx, saved.ReferenceID, stale, "TASK_FAILED", "stale failure"); err != nil || applied {
		t.Fatalf("stale failure applied=%v err=%v", applied, err)
	}
	if applied, err := repo.FailDownload(ctx, saved.ReferenceID, first, "TASK_FAILED", "download failed with HTTP 404"); err != nil || !applied {
		t.Fatalf("record failure applied=%v err=%v", applied, err)
	}
	failed, err := repo.GetReference(ctx, saved.ReferenceID)
	if err != nil || failed.DownloadErrorMessage == nil || *failed.DownloadErrorMessage != "download failed with HTTP 404" {
		t.Fatalf("failed reference = %#v, err=%v", failed, err)
	}

	second := uuid.New()
	retried, applied, err := repo.QueueDownload(ctx, saved.ReferenceID, second)
	if err != nil || !applied || retried.DownloadAttempt == nil || *retried.DownloadAttempt != second ||
		retried.DownloadErrorMessage != nil {
		t.Fatalf("retry queue = %#v, applied=%v, err=%v", retried, applied, err)
	}
	if _, applied, err := repo.LinkResolvedDocument(ctx, saved.ReferenceID, targetID, false); err != nil || !applied {
		t.Fatalf("link target applied=%v err=%v", applied, err)
	}
	if linked, applied, err := repo.QueueDownload(ctx, saved.ReferenceID, uuid.New()); err != nil || applied || linked.CitedDocumentID == nil {
		t.Fatalf("queue after link = %#v, applied=%v, err=%v", linked, applied, err)
	}
	if err := documents.Delete(ctx, targetID); err != nil {
		t.Fatalf("delete target: %v", err)
	}
	reset, err := repo.GetReference(ctx, saved.ReferenceID)
	if err != nil {
		t.Fatalf("reload reference: %v", err)
	}
	if reset.DownloadAttempt != nil || reset.DownloadRequestedAt != nil ||
		reset.DownloadErrorCode != nil || reset.DownloadErrorMessage != nil || reset.DownloadUpdatedAt != nil {
		t.Fatalf("download state was not reset after document delete: %#v", reset)
	}
}

// TestPaperCitationRepositoryMergeAutomaticCitationsPostgres verifies the
// row-locked reconciliation implementation against a real PostgreSQL database.
// It is opt-in via PAPERS_MANAGER_TEST_POSTGRES_DSN.
func TestPaperCitationRepositoryMergeAutomaticCitationsPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	repo := NewPaperCitationRepository(db)

	citingID, resolvedID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{citingID, resolvedID} {
		if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(id, "source.pdf", "citation/"+id.String(), "")); err != nil {
			t.Fatalf("create document: %v", err)
		}
	}
	automatic := testResolvedCitation(citingID, "Ada Lovelace. Notes on the analytical engine.", resolvedID)
	automatic.Origin = types.PaperCitationOriginExtraction
	if err := db.Create(&automatic).Error; err != nil {
		t.Fatalf("seed automatic citation: %v", err)
	}

	items, err := repo.MergeAutomaticCitations(ctx, citingID, []types.PaperCitation{{
		PaperReference: types.PaperReference{
			ReferenceID:      automatic.ReferenceID,
			CitingDocumentID: citingID,
			RawReference:     " [1]  ADA Lovelace. Notes on the analytical engine. ",
			Authors:          []types.PaperAuthor{},
			ResolutionStatus: types.DocumentStageSucceeded,
			MetadataSource:   types.PaperMetadataSourceExtraction,
			CitedDocumentID:  &resolvedID,
		},
		Origin: types.PaperCitationOriginExtraction,
	}})
	if err != nil {
		t.Fatalf("merge repeated automatic citation: %v", err)
	}
	if len(items) != 1 || items[0].ReferenceID != automatic.ReferenceID {
		t.Fatalf("merge result items=%#v, want prior reference", items)
	}

	manual := testResolvedCitation(citingID, "Manual reference", uuid.Nil)
	manual.Origin = types.PaperCitationOriginManual
	if _, err := repo.AddCitation(ctx, manual); err != nil {
		t.Fatalf("seed manual citation: %v", err)
	}
	items, err = repo.MergeAutomaticCitations(ctx, citingID, nil)
	if err != nil {
		t.Fatalf("merge detected empty bibliography: %v", err)
	}
	if len(items) != 1 || items[0].ReferenceID != manual.ReferenceID ||
		items[0].EffectiveOrigin() != types.PaperCitationOriginManual {
		t.Fatalf("detected empty merge retained wrong rows: %#v", items)
	}

	var count int64
	if err := db.Table("paper_citations").Where("citing_document_id = ?", citingID).Count(&count).Error; err != nil {
		t.Fatalf("count final citations: %v", err)
	}
	if count != 1 {
		t.Fatalf("final citation row count = %d, want 1", count)
	}
}
