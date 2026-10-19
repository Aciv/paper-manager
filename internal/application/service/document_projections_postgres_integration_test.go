package service

import (
	"context"
	"testing"
	"time"

	"paperMaster/internal/application/repository"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// TestReadModelScalePostgres seeds 1,000 documents with metadata, citations,
// and spans, then verifies one 200-document projection batch stays bounded.
// It is the P2-7 scale check for the fan-out fix.
func TestReadModelScalePostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, source_state, updated_at,
		 markdown_status, metadata_extraction_status)
		SELECT gen_random_uuid(), 'scale-' || lpad(n::text, 4, '0') || '.pdf',
		       'application/pdf', 1, 'scale/' || n, 'available', now(),
		       'succeeded', 'succeeded'
		FROM generate_series(1, 1000) AS n`).Error; err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	if err := db.Exec(`INSERT INTO paper_metadata (document_id, title, authors, tags)
		SELECT id, 'Scale Paper ' || original_filename, '[]'::jsonb, '[]'::jsonb
		FROM documents`).Error; err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if err := db.Exec(`INSERT INTO paper_citations
		(id, citing_document_id, reference_key, raw_reference, resolution_status,
		 resolution_updated_at, origin, created_at, updated_at)
		SELECT gen_random_uuid(), d.id, 'reference-' || n, 'Reference ' || n,
		       'not_started', now(), 'extraction', now(), now()
		FROM documents d CROSS JOIN generate_series(1, 5) AS n`).Error; err != nil {
		t.Fatalf("seed citations: %v", err)
	}
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, attempt, kind, scope, scope_id, status, started_at)
		SELECT gen_random_uuid(), id, 1, 'recognition', 'document', '', 'succeeded', now()
		FROM documents`).Error; err != nil {
		t.Fatalf("seed spans: %v", err)
	}

	var ids []uuid.UUID
	if err := db.Raw(`SELECT id FROM documents ORDER BY original_filename LIMIT 200`).Scan(&ids).Error; err != nil {
		t.Fatalf("load ids: %v", err)
	}
	documentIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		documentIDs = append(documentIDs, id.String())
	}

	documents := repository.NewDocumentRepository(db)
	metadata := repository.NewPaperMetadataRepository(db)
	citations := repository.NewPaperCitationRepository(db)
	spans := repository.NewDocumentProcessingSpanRepository(db)
	service := &documentService{
		repo:       documents,
		papers:     NewPaperService(documents, metadata, citations),
		processing: NewSpanTracker(spans),
	}

	started := time.Now()
	items, err := service.GetDocumentProjections(ctx, types.DocumentProjectionQuery{
		DocumentIDs: documentIDs, IncludeMetadata: true, IncludeProcessing: true,
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("GetDocumentProjections() error = %v", err)
	}
	if len(items) != 200 {
		t.Fatalf("projection count = %d, want 200", len(items))
	}
	for index, item := range items {
		if item.Document.ID != ids[index] {
			t.Fatalf("item %d document = %s, want %s", index, item.Document.ID, ids[index])
		}
		if item.Metadata == nil || item.Metadata.Title == "" {
			t.Fatalf("item %d metadata = %#v", index, item.Metadata)
		}
		if item.Processing == nil || item.Processing.ReferenceProgress == nil {
			t.Fatalf("item %d processing = %#v", index, item.Processing)
		}
		if item.Processing.ReferenceProgress.Queued != 5 {
			t.Fatalf("item %d queued progress = %d, want 5",
				index, item.Processing.ReferenceProgress.Queued)
		}
		if len(item.Processing.Stages) != 1 {
			t.Fatalf("item %d span count = %d, want 1", index, len(item.Processing.Stages))
		}
	}
	t.Logf("200-document projection over 1,000 documents: %s", elapsed)
	if elapsed > 10*time.Second {
		t.Fatalf("projection batch took %s, want under 10s", elapsed)
	}
}
