package repository

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDocumentNewestReplacementMovesRelationsPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	citations := NewPaperCitationRepository(db)

	olderID, newerID, sourceID, mergedChildID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	older := sourceDocumentForCanonicalTest(olderID, "older.pdf", "replacement/older", "")
	older.DOI = "10.1000/replacement"
	older.CreatedAt = time.Now().UTC().Add(-time.Hour)
	if _, err := documents.Create(ctx, older); err != nil {
		t.Fatalf("create older document: %v", err)
	}
	newer := sourceDocumentForCanonicalTest(newerID, "newer.pdf", "replacement/newer", "")
	newer.CreatedAt = time.Now().UTC()
	if _, err := documents.Create(ctx, newer); err != nil {
		t.Fatalf("create newer document: %v", err)
	}
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(
		sourceID, "source.pdf", "replacement/source", "",
	)); err != nil {
		t.Fatalf("create citing source document: %v", err)
	}
	child := sourceDocumentForCanonicalTest(mergedChildID, "child.pdf", "replacement/child", "")
	child.SourceState = types.DocumentSourceMerged
	child.CanonicalDocumentID = &olderID
	if _, err := documents.Create(ctx, child); err != nil {
		t.Fatalf("create merged child: %v", err)
	}
	if err := db.Create(&types.DocumentMarkdown{
		DocumentID: olderID, StorageKey: "replacement/older.md", Filename: "older.md",
		ContentType: "text/markdown", SizeBytes: 1, Metadata: types.JSON(`{}`),
	}).Error; err != nil {
		t.Fatalf("create older markdown: %v", err)
	}
	pageNumber, pageWidth, pageHeight := 1, 612.0, 792.0
	bboxX0, bboxY0, bboxX1, bboxY1 := 0.0, 0.0, 100.0, 20.0
	markdownStart, markdownEnd := 0, 5
	if err := db.Create(&types.Block{
		ID: uuid.New(), DocumentID: olderID, Ordinal: 0, Type: "text", Text: "older",
		AssetKey: "replacement/older.png",
		Location: &types.PDFBlockLocation{
			PageNumber: &pageNumber, PageWidth: &pageWidth, PageHeight: &pageHeight,
			BBoxX0: &bboxX0, BBoxY0: &bboxY0, BBoxX1: &bboxX1, BBoxY1: &bboxY1,
			MarkdownCodepointStart: &markdownStart, MarkdownCodepointEnd: &markdownEnd,
		},
	}).Error; err != nil {
		t.Fatalf("create older block: %v", err)
	}
	base := types.DocumentBase{ID: uuid.New(), Name: "Newest wins", Settings: types.JSON(`{}`)}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create base: %v", err)
	}
	if err := db.Create(&types.DocumentBaseFile{
		DocumentBaseID: base.ID, DocumentID: olderID, Metadata: types.JSON(`{}`),
	}).Error; err != nil {
		t.Fatalf("create base membership: %v", err)
	}
	incoming := testResolvedCitation(sourceID, "Incoming reference", olderID)
	if err := db.Create(&incoming).Error; err != nil {
		t.Fatalf("create incoming citation: %v", err)
	}
	newerSelf := testResolvedCitation(newerID, "Newest self reference", olderID)
	if err := db.Create(&newerSelf).Error; err != nil {
		t.Fatalf("create newest self citation: %v", err)
	}
	olderOutgoing := testResolvedCitation(olderID, "Older outgoing reference", sourceID)
	olderOutgoing.Origin = types.PaperCitationOriginExtraction
	if err := db.Create(&olderOutgoing).Error; err != nil {
		t.Fatalf("create older outgoing citation: %v", err)
	}

	cleanupKeys, err := documents.ReplaceOlderDocument(ctx, olderID, newerID)
	if err != nil {
		t.Fatalf("ReplaceOlderDocument: %v", err)
	}

	if _, err := documents.Get(ctx, olderID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("older document still exists, err=%v", err)
	}
	if _, err := documents.Get(ctx, mergedChildID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("merged child still exists, err=%v", err)
	}
	updatedNewer, err := documents.Get(ctx, newerID)
	if err != nil {
		t.Fatalf("load newer document: %v", err)
	}
	if updatedNewer.DOI != older.DOI {
		t.Fatalf("newest DOI = %q, want %q", updatedNewer.DOI, older.DOI)
	}
	items, err := citations.ListByCitingDocumentID(ctx, sourceID)
	if err != nil {
		t.Fatalf("list incoming citations: %v", err)
	}
	if len(items) != 1 || items[0].CitedDocumentID == nil || *items[0].CitedDocumentID != newerID {
		t.Fatalf("incoming citations = %#v, want target %s", items, newerID)
	}
	for _, owner := range []uuid.UUID{newerID, olderID} {
		remaining, listErr := citations.ListByCitingDocumentID(ctx, owner)
		if listErr != nil {
			t.Fatalf("list owner %s citations: %v", owner, listErr)
		}
		if len(remaining) != 0 {
			t.Fatalf("owner %s kept citations: %#v", owner, remaining)
		}
	}
	var membership int64
	if err := db.Model(&types.DocumentBaseFile{}).
		Where("document_base_id = ? AND document_id = ?", base.ID, newerID).
		Count(&membership).Error; err != nil {
		t.Fatalf("count newest base membership: %v", err)
	}
	if membership != 1 {
		t.Fatalf("newest base membership = %d, want 1", membership)
	}
	expectedKeys := map[string]bool{
		"replacement/older": true, "replacement/child": true,
		"replacement/older.md": true, "replacement/older.png": true,
	}
	for _, key := range cleanupKeys.StorageKeys {
		if !expectedKeys[key] {
			t.Fatalf("unexpected cleanup key %q in %#v", key, cleanupKeys.StorageKeys)
		}
		delete(expectedKeys, key)
	}
	if len(expectedKeys) != 0 {
		t.Fatalf("missing cleanup keys %#v from %#v", expectedKeys, cleanupKeys.StorageKeys)
	}
	var selfCitations int64
	if err := db.Table("paper_citations").
		Where("citing_document_id = cited_document_id").Count(&selfCitations).Error; err != nil {
		t.Fatalf("count self citations: %v", err)
	}
	if selfCitations != 0 {
		t.Fatalf("self citations after replacement = %d, want 0", selfCitations)
	}
}

// P0-2 regression: deleting a legacy canonical document must cascade its
// merged tombstones instead of being blocked by the self-referencing FK.
func TestDeleteLegacyCanonicalCascadesMergedChildrenPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)

	canonicalID, childID := uuid.New(), uuid.New()
	if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(
		canonicalID, "canonical.pdf", "legacy/canonical", "",
	)); err != nil {
		t.Fatalf("create canonical document: %v", err)
	}
	child := sourceDocumentForCanonicalTest(childID, "child.pdf", "legacy/child", "")
	child.SourceState = types.DocumentSourceMerged
	child.CanonicalDocumentID = &canonicalID
	if _, err := documents.Create(ctx, child); err != nil {
		t.Fatalf("create merged child: %v", err)
	}

	if err := documents.Delete(ctx, canonicalID); err != nil {
		t.Fatalf("delete canonical: %v", err)
	}
	for _, id := range []uuid.UUID{canonicalID, childID} {
		if _, err := documents.Get(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("document %s still exists, err=%v", id, err)
		}
	}
}

func TestDocumentNewestReplacementHasSingleWinnerPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)

	olderID, firstID, secondID := uuid.New(), uuid.New(), uuid.New()
	older := sourceDocumentForCanonicalTest(olderID, "older.pdf", "winner/older", "")
	older.DOI = "10.1000/winner"
	if _, err := documents.Create(ctx, older); err != nil {
		t.Fatalf("create older document: %v", err)
	}
	for index, id := range []uuid.UUID{firstID, secondID} {
		document := sourceDocumentForCanonicalTest(id, "candidate.pdf", "winner/"+id.String(), "")
		document.CreatedAt = time.Now().UTC().Add(time.Duration(index) * time.Second)
		if _, err := documents.Create(ctx, document); err != nil {
			t.Fatalf("create candidate %s: %v", id, err)
		}
	}

	gate := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, newestID := range []uuid.UUID{firstID, secondID} {
		wait.Add(1)
		go func(id uuid.UUID) {
			defer wait.Done()
			<-gate
			_, err := documents.ReplaceOlderDocument(ctx, olderID, id)
			errs <- err
		}(newestID)
	}
	close(gate)
	wait.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("concurrent replacement error = %v, want sql.ErrNoRows", err)
		}
	}
	if successes != 1 {
		t.Fatalf("replacement successes = %d, want exactly 1", successes)
	}
	first, err := documents.Get(ctx, firstID)
	if err != nil {
		t.Fatalf("load first candidate: %v", err)
	}
	second, err := documents.Get(ctx, secondID)
	if err != nil {
		t.Fatalf("load second candidate: %v", err)
	}
	withDOI := 0
	for _, document := range []types.Document{first, second} {
		if document.DOI == older.DOI {
			withDOI++
		}
	}
	if withDOI != 1 {
		t.Fatalf("candidates with transferred DOI = %d, want 1", withDOI)
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
