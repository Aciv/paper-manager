package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from document_base_bibliography_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestBibliographySnapshotReadsWholeBaseInStableOrderPostgres verifies that a
// bibliography snapshot rejects an undersized limit and returns every base entry
// ordered by document ID with metadata and DOI populated.
func TestBibliographySnapshotReadsWholeBaseInStableOrderPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	bases := NewDocumentBaseRepository(db)
	base := types.DocumentBase{ID: uuid.New(), Name: "Bibliography snapshot", Settings: types.JSON(`{}`)}
	if err := db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	addedAt := time.Now().UTC().Truncate(time.Microsecond)
	wantIDs := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		id := uuid.New()
		document := sourceDocumentForCanonicalTest(id, "paper.pdf", "bibliography/"+id.String(), "")
		document.DOI = "10.1000/" + id.String()
		if _, err := documents.Create(ctx, document); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&types.PaperMetadata{
			DocumentID: id, Title: "Title " + id.String(), Authors: []types.PaperAuthor{{Name: "Author"}},
			Tags: []string{}, Metadata: types.JSON(`{}`), FieldSources: types.PaperMetadataFieldSources{},
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&types.DocumentBaseFile{
			DocumentBaseID: base.ID, DocumentID: id, AddedAt: addedAt, SortOrder: 0, Metadata: types.JSON(`{}`),
		}).Error; err != nil {
			t.Fatal(err)
		}
		wantIDs = append(wantIDs, id.String())
	}
	sort.Strings(wantIDs)
	if _, err := bases.GetBibliographySnapshot(ctx, base.ID, 10); !errors.Is(err, types.ErrBibliographyBaseTooLarge) {
		t.Fatalf("small limit error = %v", err)
	}
	snapshot, err := bases.GetBibliographySnapshot(ctx, base.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 12 {
		t.Fatalf("entries = %d", len(snapshot.Entries))
	}
	for index, entry := range snapshot.Entries {
		if entry.DocumentID.String() != wantIDs[index] || entry.Metadata.Title == "" || entry.DOI == "" {
			t.Fatalf("entry %d = %#v, want id %s", index, entry, wantIDs[index])
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_blocks_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// P0-4 regression: a large parsed document exceeds pgx's 65,535 bind-parameter
// limit if every block is inserted in one statement. The parser accepts up to
// 50,000 blocks, so ReplaceParsedOutput must batch its inserts.
func TestReplaceParsedOutputPersistsLargeBlockBatchPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	documentID := createLargeBlockTestDocument(t, ctx, documents)

	initialGeneration := startMarkdownStagePostgres(t, ctx, documents, documentID)
	if err := documents.ReplaceParsedOutput(ctx,
		parsedMarkdownForTest(documentID, "large/initial.md", 1),
		parsedTestBlocks(documentID, 1, 1000), initialGeneration,
	); err != nil {
		t.Fatalf("ReplaceParsedOutput(initial): %v", err)
	}

	generation := startMarkdownStagePostgres(t, ctx, documents, documentID)
	const blockCount = 5000 // 5000 x 16 columns = 80,000 parameters in one statement
	blocks := parsedTestBlocks(documentID, blockCount, 0)
	if err := documents.ReplaceParsedOutput(ctx,
		parsedMarkdownForTest(documentID, "large/current.md", 1), blocks, generation,
	); err != nil {
		t.Fatalf("ReplaceParsedOutput(%d blocks): %v", blockCount, err)
	}

	stored, err := documents.ListBlocks(ctx, documentID, 0)
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(stored) != blockCount {
		t.Fatalf("stored blocks = %d, want %d (previous version must be replaced)", len(stored), blockCount)
	}
	if stored[0].Ordinal != 0 || stored[blockCount-1].Ordinal != blockCount-1 {
		t.Fatalf("stored ordinals = %d..%d, want 0..%d", stored[0].Ordinal, stored[blockCount-1].Ordinal, blockCount-1)
	}
	markdown, err := documents.GetMarkdown(ctx, documentID)
	if err != nil {
		t.Fatalf("GetMarkdown: %v", err)
	}
	if markdown.StorageKey != "large/current.md" {
		t.Fatalf("markdown storage key = %q, want large/current.md", markdown.StorageKey)
	}
	if markdown.RecognitionGeneration != generation {
		t.Fatalf("recognition generation = %d, want %d", markdown.RecognitionGeneration, generation)
	}
	if outline, parseErr := types.ParseDocumentOutline(markdown.DocumentOutline); parseErr != nil || outline.Source != types.DocumentOutlineNone {
		t.Fatalf("persisted outline = %#v, error=%v", outline, parseErr)
	}
	stage, err := documents.GetStage(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil {
		t.Fatalf("GetStage: %v", err)
	}
	if stage.Status != types.DocumentStageSucceeded || stage.Generation != generation {
		t.Fatalf("markdown stage = %#v, want succeeded generation %d", stage, generation)
	}
}

// A failure in a later insert batch must roll back the earlier batches together
// with the Markdown upsert and old-block deletion, so a retry never observes
// half of a parsed output.
func TestReplaceParsedOutputRollsBackPartialBlockBatchPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	documentID := createLargeBlockTestDocument(t, ctx, documents)

	initialGeneration := startMarkdownStagePostgres(t, ctx, documents, documentID)
	initialBlocks := parsedTestBlocks(documentID, 2, 100)
	if err := documents.ReplaceParsedOutput(ctx,
		parsedMarkdownForTest(documentID, "large/initial.md", 2), initialBlocks, initialGeneration,
	); err != nil {
		t.Fatalf("ReplaceParsedOutput(initial): %v", err)
	}

	generation := startMarkdownStagePostgres(t, ctx, documents, documentID)
	failingBlocks := parsedTestBlocks(documentID, 1200, 0)
	// Duplicate an ordinal inside the second insert batch (indices 500-999).
	// The first batch commits only as part of the outer transaction, so the
	// unique violation must roll every batch back.
	failingBlocks[900].Ordinal = failingBlocks[700].Ordinal
	err := documents.ReplaceParsedOutput(ctx,
		parsedMarkdownForTest(documentID, "large/failed.md", 1200), failingBlocks, generation,
	)
	if err == nil {
		t.Fatal("ReplaceParsedOutput with duplicate ordinals should fail")
	}

	stored, err := documents.ListBlocks(ctx, documentID, 0)
	if err != nil {
		t.Fatalf("ListBlocks after rollback: %v", err)
	}
	if len(stored) != len(initialBlocks) {
		t.Fatalf("blocks after rollback = %d, want previous %d", len(stored), len(initialBlocks))
	}
	for index, block := range stored {
		if block.Ordinal != initialBlocks[index].Ordinal {
			t.Fatalf("block %d ordinal after rollback = %d, want %d", index, block.Ordinal, initialBlocks[index].Ordinal)
		}
	}
	markdown, err := documents.GetMarkdown(ctx, documentID)
	if err != nil {
		t.Fatalf("GetMarkdown after rollback: %v", err)
	}
	if markdown.StorageKey != "large/initial.md" {
		t.Fatalf("markdown storage key after rollback = %q, want large/initial.md", markdown.StorageKey)
	}
	if markdown.RecognitionGeneration != initialGeneration {
		t.Fatalf("recognition generation after rollback = %d, want %d", markdown.RecognitionGeneration, initialGeneration)
	}
	stage, err := documents.GetStage(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil {
		t.Fatalf("GetStage after rollback: %v", err)
	}
	if stage.Status != types.DocumentStageRunning || stage.Generation != generation {
		t.Fatalf("markdown stage after rollback = %#v, want running generation %d", stage, generation)
	}
}

func createLargeBlockTestDocument(t *testing.T, ctx context.Context, documents interfaces.DocumentRepository) uuid.UUID {
	t.Helper()
	documentID := uuid.New()
	if _, err := documents.Create(ctx, types.Document{
		ID: documentID, OriginalFilename: "large.pdf", ContentType: "application/pdf",
		SizeBytes: 1, StorageKey: "large/" + documentID.String(), SourceState: types.DocumentSourceAvailable,
	}); err != nil {
		t.Fatalf("create document: %v", err)
	}
	return documentID
}

func startMarkdownStagePostgres(
	t *testing.T, ctx context.Context, documents interfaces.DocumentRepository, documentID uuid.UUID,
) int64 {
	t.Helper()
	state, applied, err := documents.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || !applied {
		t.Fatalf("QueueStageNewGeneration(markdown) = %#v, applied=%t, error=%v", state, applied, err)
	}
	if _, applied, err := documents.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, state.Generation); err != nil || !applied {
		t.Fatalf("TryStartStage(markdown) applied=%t, error=%v", applied, err)
	}
	return state.Generation
}

func parsedMarkdownForTest(documentID uuid.UUID, storageKey string, pageCount int) types.DocumentMarkdown {
	outline, _ := (types.DocumentOutline{SchemaVersion: types.DocumentOutlineSchemaVersion,
		Source: types.DocumentOutlineNone, Sections: []types.DocumentOutlineSection{}}).JSON()
	return types.DocumentMarkdown{
		DocumentID: documentID, StorageKey: storageKey, Filename: "document.md",
		ContentType: "text/markdown", SizeBytes: 1, PageCount: pageCount, Metadata: types.JSON(`{}`),
		DocumentOutline: outline,
	}
}

func parsedTestBlocks(documentID uuid.UUID, count, ordinalOffset int) []types.Block {
	blocks := make([]types.Block, 0, count)
	for index := 0; index < count; index++ {
		ordinal := ordinalOffset + index
		pageNumber := 1 + index/100
		pageWidth, pageHeight := 612.0, 792.0
		bboxX0, bboxY0, bboxX1, bboxY1 := 0.0, 0.0, 100.0, 20.0
		markdownStart, markdownEnd := index*10, index*10+5
		blocks = append(blocks, types.Block{
			ID: uuid.New(), DocumentID: documentID, Ordinal: ordinal, Type: "text",
			Text: fmt.Sprintf("block %d", ordinal),
			Location: &types.PDFBlockLocation{
				PageNumber: &pageNumber, PageWidth: &pageWidth, PageHeight: &pageHeight,
				BBoxX0: &bboxX0, BBoxY0: &bboxY0, BBoxX1: &bboxX1, BBoxY1: &bboxY1,
				MarkdownCodepointStart: &markdownStart, MarkdownCodepointEnd: &markdownEnd,
			},
		})
	}
	return blocks
}

// ---------------------------------------------------------------------------
// Consolidated from document_processing_span_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestDocumentProcessingSpanViewPostgres verifies that terminal spans cannot be
// reopened, the latest attempt is reported, bounded current and timeline views
// return at most 100 stages, and the ranked view can use an index.
func TestDocumentProcessingSpanViewPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documentID := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key)
		VALUES (?, 'span-view.pdf', 'application/pdf', 1, ?)`,
		documentID, "phase4/"+documentID.String()).Error; err != nil {
		t.Fatalf("insert document: %v", err)
	}
	repo := NewDocumentProcessingSpanRepository(db)

	terminal, err := repo.UpsertRunning(ctx, types.DocumentProcessingSpan{
		DocumentID: documentID, Attempt: 1, Kind: types.ProcessingSpanRecognition,
		Scope: types.ProcessingSpanScopeDocument, ScopeID: "recognition:terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, terminal.ID, types.ProcessingSpanCancelled, "CANCELLED", "safe"); err != nil {
		t.Fatal(err)
	}
	reopened, err := repo.UpsertRunning(ctx, types.DocumentProcessingSpan{
		DocumentID: documentID, Attempt: 1, Kind: types.ProcessingSpanRecognition,
		Scope: types.ProcessingSpanScopeDocument, ScopeID: "recognition:terminal", RetryCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != terminal.ID || reopened.Status != types.ProcessingSpanCancelled {
		t.Fatalf("terminal span reopened: %#v", reopened)
	}
	if latest, err := repo.LatestAttempt(ctx, documentID, types.ProcessingSpanRecognition); err != nil || latest != 1 {
		t.Fatalf("LatestAttempt(recognition) = %d, error %v", latest, err)
	}
	now := time.Now().UTC()
	rows := make([]types.DocumentProcessingSpan, 0, 750)
	for scope := 0; scope < 150; scope++ {
		scopeID := types.ProcessingSpanScopeID("provider:" + strconv.Itoa(scope))
		for attempt := 1; attempt <= 5; attempt++ {
			rows = append(rows, types.DocumentProcessingSpan{
				ID: uuid.New(), DocumentID: documentID, Attempt: attempt,
				Kind: types.ProcessingSpanCatalogProvider, Scope: types.ProcessingSpanScopeProvider,
				ScopeID: scopeID,
				Status:  types.ProcessingSpanSucceeded, Attributes: types.JSON(`{}`),
				StartedAt: now.Add(time.Duration(scope*5+attempt) * time.Millisecond),
			})
		}
	}
	if err := db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatalf("insert span history: %v", err)
	}
	current, err := repo.ListCurrent(ctx, documentID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 100 {
		t.Fatalf("bounded current stages = %d, want 100", len(current))
	}
	timeline, err := repo.ListTimeline(ctx, documentID, 1000)
	if err != nil || len(timeline) != 100 {
		t.Fatalf("bounded timeline stages = %d, error %v", len(timeline), err)
	}

	var plan strings.Builder
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		result, err := tx.Raw(`EXPLAIN (COSTS OFF)
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY document_id, kind, scope, scope_id
					ORDER BY attempt DESC, updated_at DESC, id DESC
				) AS stage_rank
				FROM document_processing_spans WHERE document_id = ?
			) ranked WHERE stage_rank = 1 LIMIT 100`, documentID).Rows()
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var line string
			if err := result.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		return result.Err()
	})
	if err != nil {
		t.Fatalf("explain bounded span view: %v", err)
	}
	if !strings.Contains(plan.String(), "Index") {
		t.Fatalf("bounded span view did not use an index:\n%s", plan.String())
	}
}

// TestListCurrentBatchAppliesLimitPerDocumentPostgres verifies that batch span
// reads apply the limit independently per document instead of across the whole
// result set.
func TestListCurrentBatchAppliesLimitPerDocumentPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	noisyID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	quietID := uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	for _, document := range []struct {
		id   uuid.UUID
		name string
	}{{noisyID, "noisy.pdf"}, {quietID, "quiet.pdf"}} {
		if _, err := documents.Create(ctx, sourceDocumentForCanonicalTest(
			document.id, document.name, "span/"+document.id.String(), "",
		)); err != nil {
			t.Fatalf("create %s: %v", document.name, err)
		}
	}
	now := time.Now().UTC()
	rows := make([]types.DocumentProcessingSpan, 0, 251)
	for index := 0; index < 250; index++ {
		rows = append(rows, types.DocumentProcessingSpan{
			ID: uuid.New(), DocumentID: noisyID, Attempt: 1,
			Kind: types.ProcessingSpanCatalogProvider, Scope: types.ProcessingSpanScopeProvider,
			ScopeID: types.ProcessingSpanScopeID("provider:" + strconv.Itoa(index)),
			Status:  types.ProcessingSpanSucceeded, Attributes: types.JSON(`{}`), StartedAt: now,
		})
	}
	rows = append(rows, types.DocumentProcessingSpan{
		ID: uuid.New(), DocumentID: quietID, Attempt: 1,
		Kind: types.ProcessingSpanRecognition, Scope: types.ProcessingSpanScopeDocument,
		ScopeID: types.ComposeProcessingSpanScopeID(types.ProcessingSpanRecognition, quietID.String()),
		Status:  types.ProcessingSpanSucceeded, Attributes: types.JSON(`{}`), StartedAt: now,
	})
	if err := db.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatalf("insert batch spans: %v", err)
	}

	grouped, err := NewDocumentProcessingSpanRepository(db).ListCurrentBatch(
		ctx, []uuid.UUID{noisyID, quietID}, 100,
	)
	if err != nil {
		t.Fatalf("ListCurrentBatch() error = %v", err)
	}
	if len(grouped[noisyID]) != 100 {
		t.Fatalf("noisy document spans = %d, want 100", len(grouped[noisyID]))
	}
	if len(grouped[quietID]) != 1 {
		t.Fatalf("quiet document spans = %d, want 1", len(grouped[quietID]))
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_processing_span_test.go.
// ---------------------------------------------------------------------------

// TestAggregateDisplayStatusPriority verifies that aggregate display status
// prioritizes active spans, then cancellation, then failure, and falls back to
// success when none apply.
func TestAggregateDisplayStatusPriority(t *testing.T) {
	tests := []struct {
		name                      string
		active, failed, cancelled int64
		want                      types.ProcessingSpanStatus
	}{
		{name: "success", want: types.ProcessingSpanSucceeded},
		{name: "failure", failed: 1, want: types.ProcessingSpanFailed},
		{name: "cancellation wins over failure", failed: 1, cancelled: 1, want: types.ProcessingSpanCancelled},
		{name: "active remains open", active: 1, failed: 1, cancelled: 1, want: types.ProcessingSpanRunning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, _, _ := aggregateDisplayStatus(test.active, test.failed, test.cancelled)
			if status != test.want {
				t.Fatalf("aggregate status = %q, want %q", status, test.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_source_lifecycle_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestDocumentNewestReplacementMovesRelationsPostgres verifies that newest-wins
// replacement transfers the DOI, incoming citation targets, and base membership
// to the newer document while deleting the older document and its legacy merged
// children. It also confirms older bibliographies and reading notes are cleaned
// up rather than transferred and that no self-citations remain.
func TestDocumentNewestReplacementMovesRelationsPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	citations := NewPaperCitationRepository(db)
	notes := NewNoteReadingRepository(db)

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
		DocumentOutline: types.JSON(`{"schemaVersion":0,"source":"unavailable","sections":[]}`),
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
	createdNote, err := notes.Put(ctx, readingNoteManifest(olderID, "replacement"), 0)
	if err != nil || createdNote.Revision != 1 {
		t.Fatalf("create older reading note: note=%#v err=%v", createdNote, err)
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
	if _, err := notes.Get(ctx, newerID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("older reading note must not transfer across document paths, error=%v", err)
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
		createdNote.Directory.Sections[0].StorageKey: true,
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

// TestDocumentNewestReplacementHasSingleWinnerPostgres verifies that concurrent
// replacements of the same older document allow exactly one winner, with the
// loser reporting no rows and only one candidate receiving the DOI.
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

// TestReferenceProgressIsDisjointPostgres verifies that reference progress maps
// each resolution status to exactly one counter, keeping queued, running,
// completed, failed, and cancelled totals disjoint.
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

// ---------------------------------------------------------------------------
// Consolidated from document_stage_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestDocumentStageRepositoryPostgres verifies stage defaults, concurrent
// queueing, cancellation fencing, generation retries, independent stage
// timestamps, and commit rejection after cancellation against PostgreSQL. It also
// checks invalid stage names and statuses plus error clearing on a new generation.
func TestDocumentStageRepositoryPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documentID := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, updated_at)
		VALUES (?, 'stages.pdf', 'application/pdf', 1, ?, ?)`,
		documentID, "stages/"+documentID.String(), time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("insert document: %v", err)
	}

	repo := NewDocumentRepository(db)
	created, err := repo.Create(ctx, types.Document{
		ID: uuid.New(), OriginalFilename: "created.pdf", ContentType: "application/pdf",
		SizeBytes: 1, StorageKey: "stages/created", SourceState: types.DocumentSourceAvailable,
	})
	if err != nil {
		t.Fatalf("Create(with stage defaults): %v", err)
	}
	if created.MarkdownStatus != types.DocumentStageNotStarted || created.MarkdownGeneration != 0 ||
		created.MetadataExtractionStatus != types.DocumentStageNotStarted || created.SourceLookupStatus != types.DocumentStageNotStarted ||
		created.ReferenceLookupStatus != types.DocumentStageNotStarted || created.MarkdownUpdatedAt.IsZero() ||
		created.MetadataExtractionUpdatedAt.IsZero() || created.SourceLookupUpdatedAt.IsZero() || created.ReferenceLookupUpdatedAt.IsZero() {
		t.Fatalf("created document stage defaults = %#v", created)
	}
	initialDocument, err := repo.Get(ctx, documentID)
	if err != nil {
		t.Fatalf("Get(initial): %v", err)
	}
	for _, stage := range []types.DocumentStage{
		types.DocumentStageMarkdown,
		types.DocumentStageMetadataExtraction,
		types.DocumentStageSourceLookup,
		types.DocumentStageReferenceLookup,
	} {
		state, err := repo.GetStage(ctx, documentID, stage)
		if err != nil || state.Status != types.DocumentStageNotStarted || state.Generation != 0 {
			t.Fatalf("initial %s state = %#v, error %v", stage, state, err)
		}
	}

	type queueResult struct {
		state   types.DocumentStageState
		applied bool
		err     error
	}
	gate := make(chan struct{})
	results := make(chan queueResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-gate
			state, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
			results <- queueResult{state, applied, err}
		}()
	}
	close(gate)
	wait.Wait()
	close(results)
	appliedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent QueueStageNewGeneration: %v", result.err)
		}
		if result.applied {
			appliedCount++
		}
		if result.state.Status != types.DocumentStageQueued || result.state.Generation != 1 {
			t.Fatalf("queued state = %#v", result.state)
		}
	}
	if appliedCount != 1 {
		t.Fatalf("concurrent queue applied count = %d, want 1", appliedCount)
	}

	started, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 1)
	if err != nil || !applied || started.Status != types.DocumentStageRunning {
		t.Fatalf("TryStartStage = %#v, applied %t, error %v", started, applied, err)
	}
	if allowed, err := repo.AllowsStageWork(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || !allowed {
		t.Fatalf("AllowsStageWork(running) = %t, error %v", allowed, err)
	}
	cancelled, applied, err := repo.CancelStage(ctx, documentID, types.DocumentStageMarkdown, 1)
	if err != nil || !applied || cancelled.Status != types.DocumentStageCancelled {
		t.Fatalf("CancelStage = %#v, applied %t, error %v", cancelled, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || applied {
		t.Fatalf("TryStartStage(after cancel) applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageMarkdown, 1, types.DocumentStageSucceeded, "", ""); err != nil || applied {
		t.Fatalf("FinishStage(after cancel) applied=%t error=%v", applied, err)
	}
	if allowed, err := repo.AllowsStageWork(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || allowed {
		t.Fatalf("AllowsStageWork(cancelled) = %t, error %v", allowed, err)
	}

	retry, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || !applied || retry.Generation != 2 || retry.Status != types.DocumentStageQueued {
		t.Fatalf("QueueStageNewGeneration(retry) = %#v, applied %t, error %v", retry, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || applied {
		t.Fatalf("old generation start applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 2); err != nil || !applied {
		t.Fatalf("new generation start applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.MarkStageRetryQueued(ctx, documentID, types.DocumentStageMarkdown, 2); err != nil || !applied {
		t.Fatalf("MarkStageRetryQueued applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 2); err != nil || !applied {
		t.Fatalf("start retry applied=%t error=%v", applied, err)
	}
	finished, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageMarkdown, 2, types.DocumentStageSucceeded, "", "")
	if err != nil || !applied || finished.Status != types.DocumentStageSucceeded {
		t.Fatalf("FinishStage = %#v, applied %t, error %v", finished, applied, err)
	}

	metadata, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMetadataExtraction)
	if err != nil || !applied || metadata.Generation != 1 {
		t.Fatalf("queue metadata = %#v, applied %t, error %v", metadata, applied, err)
	}
	markdown, err := repo.GetStage(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || markdown.Status != types.DocumentStageSucceeded || markdown.Generation != 2 {
		t.Fatalf("metadata transition changed markdown: %#v, error %v", markdown, err)
	}
	storedDocument, err := repo.Get(ctx, documentID)
	if err != nil {
		t.Fatalf("Get(after stages): %v", err)
	}
	if !storedDocument.UpdatedAt.Equal(initialDocument.UpdatedAt) {
		t.Fatalf("stage transitions changed lifecycle updated_at from %s to %s", initialDocument.UpdatedAt, storedDocument.UpdatedAt)
	}
	if !storedDocument.MetadataExtractionUpdatedAt.After(initialDocument.MetadataExtractionUpdatedAt) ||
		!storedDocument.MarkdownUpdatedAt.After(initialDocument.MarkdownUpdatedAt) {
		t.Fatal("stage transitions did not update independent timestamps")
	}
	if _, _, err := repo.FinishStage(ctx, documentID, types.DocumentStageMarkdown, 2, types.DocumentStageCancelled, "", ""); err == nil {
		t.Fatal("unsupported finish status should fail")
	}
	if _, err := repo.GetStage(ctx, documentID, types.DocumentStage("invalid")); err == nil {
		t.Fatal("invalid stage should fail")
	}

	reference, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageReferenceLookup)
	if err != nil || !applied {
		t.Fatalf("queue reference = %#v, applied %t, error %v", reference, applied, err)
	}
	type transitionResult struct {
		applied bool
		err     error
	}
	transitionGate := make(chan struct{})
	startResult, cancelResult := make(chan transitionResult, 1), make(chan transitionResult, 1)
	go func() {
		<-transitionGate
		_, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageReferenceLookup, reference.Generation)
		startResult <- transitionResult{applied, err}
	}()
	go func() {
		<-transitionGate
		_, applied, err := repo.CancelStage(ctx, documentID, types.DocumentStageReferenceLookup, reference.Generation)
		cancelResult <- transitionResult{applied, err}
	}()
	close(transitionGate)
	startTransition, cancelTransition := <-startResult, <-cancelResult
	if startTransition.err != nil || cancelTransition.err != nil || !cancelTransition.applied {
		t.Fatalf("concurrent start=%#v cancel=%#v", startTransition, cancelTransition)
	}
	storedReference, err := repo.GetStage(ctx, documentID, types.DocumentStageReferenceLookup)
	if err != nil || storedReference.Status != types.DocumentStageCancelled {
		t.Fatalf("reference after start/cancel = %#v, error %v", storedReference, err)
	}
	if _, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageReferenceLookup,
		reference.Generation, types.DocumentStageSucceeded, "", ""); err != nil || applied {
		t.Fatalf("finish reference after cancellation applied=%t error=%v", applied, err)
	}

	markdownAttempt, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || !applied {
		t.Fatalf("queue Markdown commit race = %#v, applied %t, error %v", markdownAttempt, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, markdownAttempt.Generation); err != nil || !applied {
		t.Fatalf("start Markdown commit race applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.CancelStage(ctx, documentID, types.DocumentStageMarkdown, markdownAttempt.Generation); err != nil || !applied {
		t.Fatalf("cancel Markdown commit race applied=%t error=%v", applied, err)
	}
	err = repo.ReplaceParsedOutput(ctx, types.DocumentMarkdown{
		DocumentID: documentID, StorageKey: "markdown/rejected.md", Filename: "rejected.md",
		ContentType: "text/markdown", Metadata: types.JSON(`{}`),
	}, nil, markdownAttempt.Generation)
	if !errors.Is(err, types.ErrDocumentNotProcessing) {
		t.Fatalf("ReplaceParsedOutput(after cancellation) error = %v, want ErrDocumentNotProcessing", err)
	}
	var rejectedCount int64
	if err := db.Model(&types.DocumentMarkdown{}).Where("document_id = ? AND storage_key = ?", documentID, "markdown/rejected.md").Count(&rejectedCount).Error; err != nil {
		t.Fatal(err)
	}
	if rejectedCount != 0 {
		t.Fatalf("cancelled Markdown output rows = %d, want 0", rejectedCount)
	}

	sourceAttempt, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageSourceLookup)
	if err != nil || !applied {
		t.Fatalf("queue source error test = %#v, applied %t, error %v", sourceAttempt, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageSourceLookup, sourceAttempt.Generation); err != nil || !applied {
		t.Fatalf("start source error test applied=%t error=%v", applied, err)
	}
	failed, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageSourceLookup,
		sourceAttempt.Generation, types.DocumentStageFailed, "CATALOG_UNAVAILABLE", "catalog lookup is temporarily unavailable")
	if err != nil || !applied || failed.ErrorCode == nil || *failed.ErrorCode != "CATALOG_UNAVAILABLE" ||
		failed.ErrorMessage == nil || *failed.ErrorMessage != "catalog lookup is temporarily unavailable" {
		t.Fatalf("failed stage state = %#v, applied %t, error %v", failed, applied, err)
	}
	queuedAgain, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageSourceLookup)
	if err != nil || !applied || queuedAgain.ErrorCode != nil || queuedAgain.ErrorMessage != nil {
		t.Fatalf("new generation did not clear stage error: %#v, applied %t, error %v", queuedAgain, applied, err)
	}
}
