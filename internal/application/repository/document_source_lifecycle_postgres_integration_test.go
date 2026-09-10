package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDocumentCanonicalizationPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDocumentRepository(db)

	canonicalID := uuid.New()
	if _, err := repo.Create(ctx, sourceDocumentForCanonicalTest(canonicalID, "canonical.pdf", "canonical/key", "")); err != nil {
		t.Fatalf("create canonical document: %v", err)
	}
	sourceIDs := []uuid.UUID{uuid.New(), uuid.New()}
	loserIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for i := range sourceIDs {
		if _, err := repo.Create(ctx, sourceDocumentForCanonicalTest(sourceIDs[i], "source.pdf", "source/"+sourceIDs[i].String(), "")); err != nil {
			t.Fatalf("create citing document: %v", err)
		}
		if _, _, err := repo.EnsureUnknown(ctx, loserIDs[i]); err != nil {
			t.Fatalf("create placeholder: %v", err)
		}
		if _, applied, err := repo.QueueStageNewGeneration(ctx, loserIDs[i], types.DocumentStageSourceLookup); err != nil || !applied {
			t.Fatalf("queue placeholder source lookup applied=%t error=%v", applied, err)
		}
		citation := types.PaperCitation{
			CitingDocumentID: sourceIDs[i], CitedDocumentID: loserIDs[i],
			RawReference: "shared paper", Origin: types.PaperCitationOriginExtraction,
		}
		if err := db.Create(&citation).Error; err != nil {
			t.Fatalf("create citation: %v", err)
		}
	}

	gate := make(chan struct{})
	errs := make(chan error, len(loserIDs))
	var wait sync.WaitGroup
	for _, loserID := range loserIDs {
		wait.Add(1)
		go func(id uuid.UUID) {
			defer wait.Done()
			<-gate
			errs <- repo.MergeIntoCanonical(ctx, id, canonicalID)
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

	for _, loserID := range loserIDs {
		loser, err := repo.Get(ctx, loserID)
		if err != nil {
			t.Fatalf("load loser: %v", err)
		}
		if loser.SourceState != types.DocumentSourceMerged || loser.CanonicalDocumentID == nil || *loser.CanonicalDocumentID != canonicalID {
			t.Fatalf("loser lifecycle = %#v", loser)
		}
		if _, applied, err := repo.TryStartStage(ctx, loserID, types.DocumentStageSourceLookup, 1); err != nil || applied {
			t.Fatalf("late loser stage start applied=%t error=%v", applied, err)
		}
		if err := repo.MergeIntoCanonical(ctx, loserID, canonicalID); err != nil {
			t.Fatalf("idempotent merge: %v", err)
		}
	}
	var canonicalEdges, loserEdges int64
	if err := db.Model(&types.PaperCitation{}).Where("cited_document_id = ?", canonicalID).Count(&canonicalEdges).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&types.PaperCitation{}).Where("cited_document_id IN ? OR citing_document_id IN ?", loserIDs, loserIDs).Count(&loserEdges).Error; err != nil {
		t.Fatal(err)
	}
	if canonicalEdges != int64(len(sourceIDs)) || loserEdges != 0 {
		t.Fatalf("citation redirect canonical=%d loser=%d", canonicalEdges, loserEdges)
	}
}

func TestConcurrentPlaceholderPromotionUsesHashCanonical(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDocumentRepository(db)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	for _, id := range ids {
		if _, _, err := repo.EnsureUnknown(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	type result struct {
		id       uuid.UUID
		promoted bool
		err      error
	}
	results := make(chan result, 2)
	gate := make(chan struct{})
	for _, id := range ids {
		go func(documentID uuid.UUID) {
			<-gate
			promoted, err := repo.PromoteUnknown(ctx, sourceDocumentForCanonicalTest(
				documentID, "same.pdf", "download/"+documentID.String(), hash,
			))
			results <- result{documentID, promoted, err}
		}(id)
	}
	close(gate)
	first, second := <-results, <-results
	all := []result{first, second}
	var canonical, loser uuid.UUID
	for _, item := range all {
		switch {
		case item.err == nil && item.promoted:
			canonical = item.id
		case errors.Is(item.err, types.ErrDuplicateFileHash):
			loser = item.id
		default:
			t.Fatalf("promotion result = %#v", item)
		}
	}
	if canonical == uuid.Nil || loser == uuid.Nil {
		t.Fatalf("canonical=%s loser=%s", canonical, loser)
	}
	if err := repo.MergeIntoCanonical(ctx, loser, canonical); err != nil {
		t.Fatalf("merge hash loser: %v", err)
	}
	var available int64
	if err := db.Model(&types.Document{}).Where("file_sha256 = ? AND source_state = ?", hash, types.DocumentSourceAvailable).Count(&available).Error; err != nil {
		t.Fatal(err)
	}
	if available != 1 {
		t.Fatalf("available canonical rows = %d, want 1", available)
	}
}

func TestCitationTargetProgressIsBoundedAndDisjointPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDocumentRepository(db)
	sourceID := uuid.New()
	if _, err := repo.Create(ctx, sourceDocumentForCanonicalTest(sourceID, "source.pdf", "progress/source", "")); err != nil {
		t.Fatal(err)
	}
	targets := map[types.DocumentStageStatus]uuid.UUID{
		types.DocumentStageQueued: uuid.New(), types.DocumentStageRunning: uuid.New(),
		types.DocumentStageFailed: uuid.New(), types.DocumentStageCancelled: uuid.New(),
	}
	for status, id := range targets {
		if _, _, err := repo.EnsureUnknown(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&types.Document{}).Where("id = ?", id).Updates(map[string]interface{}{
			"source_lookup_status": status, "source_lookup_generation": 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&types.PaperCitation{CitingDocumentID: sourceID, CitedDocumentID: id, RawReference: string(status)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	availableID := uuid.New()
	if _, err := repo.Create(ctx, sourceDocumentForCanonicalTest(availableID, "available.pdf", "progress/available", "")); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&types.PaperCitation{CitingDocumentID: sourceID, CitedDocumentID: availableID, RawReference: "available"}).Error; err != nil {
		t.Fatal(err)
	}

	progress, err := repo.CitationTargetProgress(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Queued != 1 || progress.Running != 1 || progress.Completed != 1 || progress.Failed != 1 || progress.Cancelled != 1 {
		t.Fatalf("citation target progress = %#v", progress)
	}
}

func sourceDocumentForCanonicalTest(id uuid.UUID, filename, storageKey, hash string) types.Document {
	return types.Document{
		ID: id, OriginalFilename: filename, ContentType: "application/pdf", SizeBytes: 1,
		StorageKey: storageKey, FileSHA256: hash,
		SourceState: types.DocumentSourceAvailable,
	}
}
