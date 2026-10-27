package repository

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"testing"

	"github.com/google/uuid"
	"paperMaster/internal/types"
)

func TestSourceSectionBlockRange(t *testing.T) {
	outline := types.DocumentOutline{Sections: []types.DocumentOutlineSection{
		{Ordinal: 0, Level: 1, StartBlockOrdinal: 0, EndBlockOrdinal: 10},
		{Ordinal: 1, Level: 2, StartBlockOrdinal: 2, EndBlockOrdinal: 6},
		{Ordinal: 2, Level: 2, StartBlockOrdinal: 6, EndBlockOrdinal: 10},
		{Ordinal: 3, Level: 1, StartBlockOrdinal: 10, EndBlockOrdinal: 12},
	}}
	for _, test := range []struct{ ordinal, start, end int }{{0, 0, 10}, {1, 2, 6}, {2, 6, 10}, {3, 10, 12}} {
		_, r, err := sourceSectionBlockRange(outline, test.ordinal)
		if err != nil || r.Start != test.start || r.End != test.end {
			t.Fatalf("%+v: %+v %v", test, r, err)
		}
	}
	outline.Sections[3].StartBlockOrdinal = -1
	if _, _, err := sourceSectionBlockRange(outline, 0); !errors.Is(err, types.ErrSourceRangeUnavailable) {
		t.Fatalf("unmapped successor: %v", err)
	}
	if _, _, err := sourceSectionBlockRange(outline, 99); !errors.Is(err, types.ErrInvalidBlockQuery) {
		t.Fatal(err)
	}
}

func TestQueryBlocksPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	id, other := uuid.New(), uuid.New()
	for _, docID := range []uuid.UUID{id, other} {
		doc := sourceDocumentForCanonicalTest(
			docID, "paper.pdf", "pdf/"+docID.String(), "",
		)
		// An in-flight new generation must not replace the committed manifest version.
		doc.MarkdownGeneration = 8
		if err := db.Create(&doc).Error; err != nil {
			t.Fatal(err)
		}
	}
	outline := types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlinePDF,
		Sections: []types.DocumentOutlineSection{
			{Ordinal: 0, Level: 1, StartBlockOrdinal: 0, EndBlockOrdinal: 4},
			{Ordinal: 1, Level: 2, StartBlockOrdinal: 1, EndBlockOrdinal: 4},
			{Ordinal: 2, Level: 1, StartBlockOrdinal: 4, EndBlockOrdinal: 6},
		},
	}
	encoded, err := outline.JSON()
	if err != nil {
		t.Fatal(err)
	}
	md := types.DocumentMarkdown{DocumentID: id, StorageKey: "markdown/" + id.String(), Filename: "paper.md",
		RecognitionGeneration: 7,
		Metadata:              types.JSON(`{}`), DocumentOutline: encoded}
	if err := db.Create(&md).Error; err != nil {
		t.Fatal(err)
	}
	blocks := parsedTestBlocks(id, 6, 0)
	foreign := parsedTestBlocks(other, 1, 0)[0]
	blocks = append(blocks, foreign)
	if err := db.Create(&blocks).Error; err != nil {
		t.Fatal(err)
	}
	// Missing manifests are omitted; the batch remains one SELECT regardless of IDs.
	counter := &outlineSelectCounter{}
	countRepo := NewDocumentRepository(db.Session(&gorm.Session{Logger: counter}))
	counts, err := countRepo.MarkdownPageCounts(ctx, []uuid.UUID{id, other, uuid.New()})
	if err != nil || len(counts) != 1 || counter.selects.Load() != 1 {
		t.Fatalf("page count projection: %+v selects=%d err=%v", counts, counter.selects.Load(), err)
	}
	repo := NewDocumentRepository(db)
	ordinal := 0
	gen := int64(7)
	q := types.DocumentBlockQuery{SectionOrdinal: &ordinal, Mode: "blocks", Page: 1, PageSize: 2, ExpectedGeneration: &gen}
	result, err := repo.QueryBlocks(ctx, id, q)
	if err != nil || result.RecognitionGeneration != 7 || result.Total != 4 ||
		!result.HasMore || len(result.Items) != 2 || result.Items[1].Ordinal != 1 {
		t.Fatalf("section page: %+v %v", result, err)
	}
	q.Page = 2
	result, err = repo.QueryBlocks(ctx, id, q)
	if err != nil || result.HasMore || result.Items[0].Ordinal != 2 {
		t.Fatalf("second page: %+v %v", result, err)
	}
	q.Mode = "range"
	q.Page = 1
	result, err = repo.QueryBlocks(ctx, id, q)
	if err != nil || result.Range.End != 4 || len(result.Items) != 0 || len(result.BlockIDs) != 0 {
		t.Fatalf("range: %+v %v", result, err)
	}
	q.IncludeIDs = true
	result, err = repo.QueryBlocks(ctx, id, q)
	if err != nil || len(result.BlockIDs) != 2 || !result.HasMore {
		t.Fatalf("IDs: %+v %v", result, err)
	}
	q = types.DocumentBlockQuery{Mode: "blocks", Page: 1, PageSize: 1,
		BlockIDs: []string{blocks[2].ID.String(), foreign.ID.String(), blocks[0].ID.String()},
	}
	result, err = repo.QueryBlocks(ctx, id, q)
	if err != nil || result.Total != 2 || result.Status != "partial" ||
		len(result.MissingIDs) != 1 || result.Items[0].Ordinal != 0 {
		t.Fatalf("scoped IDs: %+v %v", result, err)
	}
	q.BlockIDs = []string{foreign.ID.String()}
	result, err = repo.QueryBlocks(ctx, id, q)
	if err != nil || result.Status != "failed" {
		t.Fatalf("foreign IDs: %+v %v", result, err)
	}
	q.BlockIDs = nil
	q.Range = &types.BlockRange{Start: 4, End: 6}
	result, err = repo.QueryBlocks(ctx, id, q)
	if err != nil || result.Total != 2 || result.Items[0].Ordinal != 4 {
		t.Fatalf("range blocks: %+v %v", result, err)
	}
	gen = 8
	q.ExpectedGeneration = &gen
	if _, err := repo.QueryBlocks(ctx, id, q); !errors.Is(err, types.ErrSourceSnapshotChanged) {
		t.Fatalf("version: %v", err)
	}
	// Commit a replacement between the manifest SELECT and block SELECT. The
	// repeatable-read result must still contain the complete old set of blocks.
	if err := db.Callback().Query().After("gorm:query").Register("test_replace_parse", func(tx *gorm.DB) {
		if tx.Statement.Table != "document_markdowns" {
			return
		}
		err := db.Transaction(func(writer *gorm.DB) error {
			if err := writer.Where("document_id = ?", id).Delete(&types.Block{}).Error; err != nil {
				return err
			}
			if err := writer.Model(&types.DocumentMarkdown{}).Where("document_id = ?", id).
				Update("recognition_generation", 9).Error; err != nil {
				return err
			}
			return writer.Create(parsedTestBlocks(id, 1, 0)).Error
		})
		if err != nil {
			t.Errorf("concurrent replacement: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	q = types.DocumentBlockQuery{Mode: "blocks", Page: 1, PageSize: 20}
	result, err = repo.QueryBlocks(ctx, id, q)
	db.Callback().Query().Remove("test_replace_parse")
	if err != nil || result.RecognitionGeneration != 7 || result.Total != 6 || len(result.Items) != 6 {
		t.Fatalf("mixed snapshot: %+v %v", result, err)
	}
	gen = 7
	q.ExpectedGeneration = &gen
	if _, err := repo.QueryBlocks(ctx, id, q); !errors.Is(err, types.ErrSourceSnapshotChanged) {
		t.Fatalf("stale IDs: %v", err)
	}

}
