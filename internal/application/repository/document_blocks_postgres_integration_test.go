package repository

import (
	"context"
	"fmt"
	"testing"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

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
