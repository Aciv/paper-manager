package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestPaperMetadataSearchUsesIndexedAuthorProjectionPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	metadata := NewPaperMetadataRepository(db)

	matchingID, otherID := uuid.New(), uuid.New()
	for _, document := range []types.Document{
		sourceDocumentForCanonicalTest(matchingID, "matching.pdf", "search/matching", ""),
		sourceDocumentForCanonicalTest(otherID, "other.pdf", "search/other", ""),
	} {
		if _, err := documents.Create(ctx, document); err != nil {
			t.Fatalf("create document: %v", err)
		}
	}
	if _, err := metadata.Upsert(ctx, matchingID, types.PaperMetadataUpdate{
		Title: stringPointerForTest("Graph Neural Networks"),
		Authors: &[]types.PaperAuthor{
			{Name: "Ada Lovelace"},
			{Name: "Grace Hopper"},
		},
	}); err != nil {
		t.Fatalf("upsert matching metadata: %v", err)
	}
	if _, err := metadata.Upsert(ctx, otherID, types.PaperMetadataUpdate{
		Title:   stringPointerForTest("Unrelated Systems Paper"),
		Authors: &[]types.PaperAuthor{{Name: "Alan Turing"}},
	}); err != nil {
		t.Fatalf("upsert other metadata: %v", err)
	}

	stored, err := metadata.GetByDocumentID(ctx, matchingID)
	if err != nil {
		t.Fatalf("get metadata: %v", err)
	}
	if stored.AuthorNames != "Ada Lovelace\nGrace Hopper" {
		t.Fatalf("author_names = %q, want trigger-maintained author names", stored.AuthorNames)
	}

	titlePage, err := metadata.Search(ctx, types.PaperSearch{Title: "neural", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("search title: %v", err)
	}
	if len(titlePage.Items) != 1 || titlePage.Items[0].DocumentID != matchingID {
		t.Fatalf("title search = %#v, want only %s", titlePage.Items, matchingID)
	}
	authorPage, err := metadata.Search(ctx, types.PaperSearch{Author: "hopper", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("search author: %v", err)
	}
	if len(authorPage.Items) != 1 || authorPage.Items[0].DocumentID != matchingID {
		t.Fatalf("author search = %#v, want only %s", authorPage.Items, matchingID)
	}

	// The trigger must also keep the projection current when authors change.
	if _, err := metadata.Upsert(ctx, matchingID, types.PaperMetadataUpdate{
		Authors: &[]types.PaperAuthor{{Name: "Grace Hopper"}},
	}); err != nil {
		t.Fatalf("update authors: %v", err)
	}
	stored, err = metadata.GetByDocumentID(ctx, matchingID)
	if err != nil {
		t.Fatalf("reload metadata: %v", err)
	}
	if stored.AuthorNames != "Grace Hopper" {
		t.Fatalf("author_names after update = %q, want %q", stored.AuthorNames, "Grace Hopper")
	}

	for _, planCase := range []struct {
		name  string
		query string
		args  []interface{}
		index string
	}{
		{
			name:  "title",
			query: "SELECT document_id FROM paper_metadata WHERE title ILIKE ?",
			args:  []interface{}{"%neural%"},
			index: "idx_paper_metadata_title_trgm",
		},
		{
			name:  "author",
			query: "SELECT document_id FROM paper_metadata WHERE author_names ILIKE ?",
			args:  []interface{}{"%hopper%"},
			index: "idx_paper_metadata_author_names_trgm",
		},
		{
			name:  "updated_at ordering",
			query: "SELECT document_id FROM paper_metadata ORDER BY updated_at DESC, document_id DESC LIMIT 10",
			index: "idx_paper_metadata_updated_at",
		},
	} {
		plan := explainPlanForTest(t, db, planCase.query, planCase.args...)
		if !strings.Contains(plan, planCase.index) {
			t.Fatalf("%s search plan does not use %s:\n%s", planCase.name, planCase.index, plan)
		}
	}
}

func TestSearchTitleSeedsAndFindAvailableByDOIsPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	metadata := NewPaperMetadataRepository(db)

	graphOneID, graphTwoID, unrelatedID, deletedID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, document := range []types.Document{
		sourceDocumentForCanonicalTest(graphOneID, "graph-one.pdf", "batch/graph-one", ""),
		sourceDocumentForCanonicalTest(graphTwoID, "graph-two.pdf", "batch/graph-two", ""),
		sourceDocumentForCanonicalTest(unrelatedID, "unrelated.pdf", "batch/unrelated", ""),
		sourceDocumentForCanonicalTest(deletedID, "deleted.pdf", "batch/deleted", ""),
	} {
		if document.ID == graphOneID {
			document.DOI = "10.1000/batch-one"
		}
		if document.ID == deletedID {
			document.SourceState = types.DocumentSourceDeleting
			document.DOI = "10.1000/batch-deleted"
		}
		if _, err := documents.Create(ctx, document); err != nil {
			t.Fatalf("create document: %v", err)
		}
	}
	for _, row := range []struct {
		documentID uuid.UUID
		title      string
	}{
		{graphOneID, "Graph Neural Networks"},
		{graphTwoID, "Graph Search"},
		{unrelatedID, "Unrelated Systems Paper"},
		{deletedID, "Graph Hidden"},
	} {
		if _, err := metadata.Upsert(ctx, row.documentID, types.PaperMetadataUpdate{
			Title: stringPointerForTest(row.title),
		}); err != nil {
			t.Fatalf("upsert metadata: %v", err)
		}
	}

	bySeed, err := metadata.SearchTitleSeeds(ctx, []string{"Graph", "Unrelated", "Missing"}, uuid.Nil, 10)
	if err != nil {
		t.Fatalf("SearchTitleSeeds() error = %v", err)
	}
	if len(bySeed["Graph"]) != 2 {
		t.Fatalf("graph candidates = %#v, want two available documents", bySeed["Graph"])
	}
	if len(bySeed["Unrelated"]) != 1 || bySeed["Unrelated"][0].DocumentID != unrelatedID {
		t.Fatalf("unrelated candidates = %#v", bySeed["Unrelated"])
	}
	if _, exists := bySeed["Missing"]; exists {
		t.Fatalf("unexpected candidates for an unmatched seed: %#v", bySeed["Missing"])
	}
	excluded, err := metadata.SearchTitleSeeds(ctx, []string{"Graph"}, graphOneID, 10)
	if err != nil {
		t.Fatalf("SearchTitleSeeds(exclude) error = %v", err)
	}
	if len(excluded["Graph"]) != 1 || excluded["Graph"][0].DocumentID != graphTwoID {
		t.Fatalf("excluded graph candidates = %#v", excluded["Graph"])
	}
	limited, err := metadata.SearchTitleSeeds(ctx, []string{"Unrelated"}, uuid.Nil, 1)
	if err != nil || len(limited["Unrelated"]) != 1 {
		t.Fatalf("per-seed limit = %#v, err=%v", limited["Unrelated"], err)
	}

	matches, err := documents.FindAvailableByDOIs(ctx, []string{"10.1000/batch-one", "10.1000/batch-deleted"}, uuid.Nil)
	if err != nil {
		t.Fatalf("FindAvailableByDOIs() error = %v", err)
	}
	if len(matches) != 1 || matches["10.1000/batch-one"] != graphOneID {
		t.Fatalf("DOI matches = %#v, want only the available document", matches)
	}
	excludedMatches, err := documents.FindAvailableByDOIs(ctx, []string{"10.1000/batch-one"}, graphOneID)
	if err != nil || len(excludedMatches) != 0 {
		t.Fatalf("excluded DOI matches = %#v, err=%v", excludedMatches, err)
	}
}

func TestPaperSearchScalePostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, source_state, updated_at)
		SELECT gen_random_uuid(), 'scale-' || n || '.pdf', 'application/pdf', 1,
		       'scale/' || n, 'available', now()
		FROM generate_series(1, 10000) AS n`).Error; err != nil {
		t.Fatalf("seed scale documents: %v", err)
	}
	if err := db.Exec(`INSERT INTO paper_metadata (document_id, title, authors, tags)
		SELECT id, 'Scale Paper ' || regexp_replace(original_filename, '[^0-9]', '', 'g'),
		       '[{"name":"Scale Author"}]'::jsonb, '[]'::jsonb
		FROM documents`).Error; err != nil {
		t.Fatalf("seed scale metadata: %v", err)
	}
	repo := NewPaperMetadataRepository(db)
	started := time.Now()
	page, err := repo.Search(ctx, types.PaperSearch{Title: "paper 9999", Page: 1, PageSize: 10})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("search total/items = %d/%d, want 1/1", page.Total, len(page.Items))
	}
	t.Logf("trigram title search over 10,000 rows: %s", elapsed)
	if elapsed > 5*time.Second {
		t.Fatalf("search took %s, want under 5s", elapsed)
	}
}

func explainPlanForTest(t *testing.T, db *gorm.DB, query string, args ...interface{}) string {
	t.Helper()
	var plan strings.Builder
	err := db.Transaction(func(tx *gorm.DB) error {
		// Tiny fixture tables always invite a sequential scan. Disable that
		// option so the assertion checks that an index can serve the query at
		// all, not that the planner prefers it at this data volume.
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		rows, err := tx.Raw("EXPLAIN (FORMAT TEXT) "+query, args...).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain %q: %v", query, err)
	}
	return plan.String()
}

func stringPointerForTest(value string) *string {
	return &value
}
