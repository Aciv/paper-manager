package postgres

import (
	"strings"
	"testing"
)

func TestDocumentsMigrationContainsProjectTables(t *testing.T) {
	for _, table := range []string{
		"documents",
		"document_blocks",
	} {
		if !strings.Contains(documentsUp, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("documents migration does not create %q", table)
		}
	}
	for _, removedTable := range []string{"document_operations", "processing_tasks", "document_captures"} {
		if strings.Contains(documentsUp, removedTable) {
			t.Fatalf("documents migration still creates removed table %q", removedTable)
		}
	}
	if !strings.Contains(documentsUp, "url TEXT NOT NULL DEFAULT ''") {
		t.Fatal("documents migration does not include document_blocks.url")
	}
	if !strings.Contains(documentsUp, "type NOT IN ('image', 'table') OR url <> ''") {
		t.Fatal("documents migration does not require URLs for image and table blocks")
	}
}

func TestHardeningMigrationAddsDatabaseGuards(t *testing.T) {
	for _, fragment := range []string{"CREATE TABLE IF NOT EXISTS task_dead_letters", "uq_documents_doi_nonempty", "idx_task_dead_letters_scope_cursor", "'deleting'"} {
		if !strings.Contains(documentHardeningUp, fragment) {
			t.Fatalf("hardening migration does not include %q", fragment)
		}
	}
}

func TestCancelledMigrationAddsCancelledStatus(t *testing.T) {
	if !strings.Contains(documentCancelledUp, "'cancelled'") {
		t.Fatal("cancelled migration does not add the cancelled document status")
	}
}

func TestProcessingRunItemsMigrationSeparatesFanoutControlAndIndexesView(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS processing_run_items",
		"ADD COLUMN IF NOT EXISTS items_sealed_at",
		"idx_processing_run_items_active",
		"idx_document_processing_spans_current_view",
		"idx_processing_runs_owner_view",
	} {
		if !strings.Contains(processingRunItemsAndSpanViewUp, fragment) {
			t.Fatalf("processing run items migration does not include %q", fragment)
		}
	}
}

func TestDocumentStageControlMigrationAddsFourIndependentStages(t *testing.T) {
	for _, fragment := range []string{
		"markdown_status TEXT NOT NULL DEFAULT 'not_started'",
		"metadata_extraction_generation BIGINT NOT NULL DEFAULT 0",
		"source_lookup_updated_at TIMESTAMPTZ NOT NULL",
		"reference_lookup_status TEXT NOT NULL DEFAULT 'not_started'",
		"idx_documents_markdown_stage_active",
		"metadata_source = 'extraction'",
		"scope = 'reference_run'",
	} {
		if !strings.Contains(documentStageControlUp, fragment) {
			t.Fatalf("document stage control migration does not include %q", fragment)
		}
	}
}

func TestProcessingSpanRetentionMigrationUsesTerminalAndParentIndexes(t *testing.T) {
	for _, fragment := range []string{"idx_document_processing_spans_terminal_retention", "finished_at ASC, id ASC", "idx_document_processing_spans_parent_active"} {
		if !strings.Contains(processingSpanRetentionUp, fragment) {
			t.Fatalf("processing span retention migration does not include %q", fragment)
		}
	}
}

func TestLegacyProcessingControlRemovalIsExplicitAndIrreversible(t *testing.T) {
	for _, fragment := range []string{
		"DROP COLUMN IF EXISTS run_id",
		"DROP TABLE IF EXISTS processing_run_items",
		"DROP TABLE IF EXISTS processing_runs",
		"DROP COLUMN IF EXISTS error_message",
		"DROP COLUMN IF EXISTS status",
	} {
		if !strings.Contains(removeLegacyProcessingControlUp, fragment) {
			t.Fatalf("legacy control removal migration does not include %q", fragment)
		}
	}
	if !strings.Contains(removeLegacyProcessingControlDown, "Intentionally irreversible") {
		t.Fatal("legacy control removal migration must document its irreversible rollback")
	}
}

func TestPaperMetadataAndBasesMigrationCreatesNormalizedTables(t *testing.T) {
	for _, fragment := range []string{
		"RENAME COLUMN filename TO original_filename",
		"CREATE TABLE IF NOT EXISTS paper_metadata",
		"tags JSONB NOT NULL DEFAULT '[]'::jsonb",
		"metadata_source TEXT NOT NULL DEFAULT 'unknown'",
		"CREATE TABLE IF NOT EXISTS document_bases",
		"CREATE TABLE IF NOT EXISTS document_base_files",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(paperMetadataAndBasesUp, fragment) {
			t.Fatalf("paper metadata/base migration does not include %q", fragment)
		}
	}
}

func TestPaperMetadataSourceMigrationAddsOriginPrecedenceField(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS metadata_source",
		"'unknown', 'extraction', 'catalog', 'manual'",
		"chk_paper_metadata_source",
	} {
		if !strings.Contains(paperMetadataSourceUp, fragment) {
			t.Fatalf("paper metadata source migration does not include %q", fragment)
		}
	}
}

func TestPaperMetadataFieldSourcesMigrationProtectsHistoricalManualEdits(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS field_sources JSONB",
		"WHEN 'manual' THEN jsonb_build_object",
		"'title', 'manual'",
		"WHERE field_sources = '{}'::jsonb",
	} {
		if !strings.Contains(paperMetadataFieldSourcesUp, fragment) {
			t.Fatalf("field sources migration does not include %q", fragment)
		}
	}
}

func TestDocumentBaseFileRenameMigrationSupportsExistingDeployments(t *testing.T) {
	for _, fragment := range []string{"document_base_documents", "document_base_files", "RENAME TO document_base_files"} {
		if !strings.Contains(documentBaseFilesRenameUp, fragment) {
			t.Fatalf("document base file rename migration does not include %q", fragment)
		}
	}
}

func TestPaperMetadataTagsIndexMigration(t *testing.T) {
	if !strings.Contains(paperMetadataTagsIndexUp, "USING GIN (tags jsonb_path_ops)") {
		t.Fatal("paper metadata tag migration does not create a GIN JSONB index")
	}
}

func TestDocumentMarkdownMigrationCreatesDurableSourceManifest(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS document_markdowns",
		"document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE",
		"storage_key TEXT NOT NULL UNIQUE",
		"content_type TEXT NOT NULL DEFAULT 'text/markdown'",
		"page_count INTEGER NOT NULL DEFAULT 0",
	} {
		if !strings.Contains(documentMarkdownsUp, fragment) {
			t.Fatalf("document markdown migration does not include %q", fragment)
		}
	}
	if strings.Contains(documentMarkdownsUp, "asset_count") {
		t.Fatal("document markdown migration must not include asset_count")
	}
}

func TestDocumentBlockSourceMapMigrationAddsTypedPDFLocation(t *testing.T) {
	for _, fragment := range []string{
		"page_width DOUBLE PRECISION",
		"bbox_x0 DOUBLE PRECISION",
		"markdown_codepoint_start INTEGER",
		"idx_document_blocks_page",
	} {
		if !strings.Contains(documentBlockSourceMapUp, fragment) {
			t.Fatalf("document block source-map migration does not include %q", fragment)
		}
	}
}

func TestRemoveMarkdownSHA256MigrationDropsDerivedRevisionColumns(t *testing.T) {
	for _, fragment := range []string{
		"ALTER TABLE document_markdowns DROP COLUMN IF EXISTS sha256",
		"ALTER TABLE document_blocks DROP COLUMN IF EXISTS markdown_sha256",
	} {
		if !strings.Contains(removeMarkdownSHA256Up, fragment) {
			t.Fatalf("remove markdown SHA-256 migration does not include %q", fragment)
		}
	}
}

func TestDocumentBlockAssetMigrationKeepsStorageKeyPrivate(t *testing.T) {
	if !strings.Contains(documentBlockAssetsUp, "asset_key TEXT NOT NULL DEFAULT ''") {
		t.Fatal("document block asset migration does not add asset_key")
	}
}

func TestBibliographyOrderMigrationAddsNullableOrderedIndex(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS bibliography_order INTEGER",
		"bibliography_order IS NULL OR bibliography_order >= 0",
		"idx_paper_citations_bibliography_order",
	} {
		if !strings.Contains(bibliographyOrderUp, fragment) {
			t.Fatalf("bibliography order migration does not include %q", fragment)
		}
	}
	if !strings.Contains(bibliographyOrderDown, "DROP COLUMN IF EXISTS bibliography_order") {
		t.Fatal("bibliography order rollback does not drop the column")
	}
}

func TestDocumentOutlineMigrationAddsExplicitGenerationBoundManifest(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS document_outline JSONB",
		"legacy_missing_outline",
		"ADD COLUMN IF NOT EXISTS recognition_generation BIGINT",
		"jsonb_typeof(document_outline) = 'object'",
	} {
		if !strings.Contains(documentOutlineUp, fragment) {
			t.Fatalf("document outline migration does not include %q", fragment)
		}
	}
}

func TestDocumentReadingNotesMigrationCreatesBoundedRevisionedNote(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS document_reading_notes",
		"document_id UUID PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE",
		"structure JSONB NOT NULL",
		"revision BIGINT NOT NULL DEFAULT 1",
		"size_bytes > 0 AND size_bytes <= 10485760",
	} {
		if !strings.Contains(documentReadingNotesUp, fragment) {
			t.Fatalf("document reading notes migration does not include %q", fragment)
		}
	}
	if strings.Contains(documentReadingNotesUp, "markdown TEXT") {
		t.Fatal("document reading notes migration must store a file manifest, not Markdown text")
	}
	if strings.Contains(documentReadingNotesUp, "storage_key TEXT") {
		t.Fatal("document reading notes root must not store one monolithic file key")
	}
}

func TestPaperCitationsMigrationCreatesDirectionalCascadeRelation(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS paper_citations",
		"citing_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE",
		"cited_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE",
		"raw_reference TEXT NOT NULL",
		"PRIMARY KEY (citing_document_id, cited_document_id)",
		"CHECK (citing_document_id <> cited_document_id)",
		"idx_paper_citations_cited_document_id",
	} {
		if !strings.Contains(paperCitationsUp, fragment) {
			t.Fatalf("paper citations migration does not include %q", fragment)
		}
	}
}

func TestPaperCitationOriginsMigrationProtectsHistoricalEdges(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'manual'",
		"chk_paper_citations_origin",
		"'manual', 'extraction'",
		"idx_paper_citations_automatic_owner",
	} {
		if !strings.Contains(paperCitationOriginsUp, fragment) {
			t.Fatalf("citation origins migration does not include %q", fragment)
		}
	}
}

func TestUnknownCitationsMigrationAddsPlaceholderStatusAndRawEvidence(t *testing.T) {
	for _, fragment := range []string{
		"'unknown'",
		"ADD COLUMN IF NOT EXISTS raw_reference TEXT NOT NULL DEFAULT ''",
		"ALTER COLUMN raw_reference DROP DEFAULT",
	} {
		if !strings.Contains(unknownCitationsUp, fragment) {
			t.Fatalf("unknown citations migration does not include %q", fragment)
		}
	}
}

func TestDocumentFileHashMigrationAddsDeduplicationGuard(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS file_sha256 CHAR(64) NOT NULL DEFAULT ''",
		"uq_documents_file_sha256_nonempty",
		"WHERE file_sha256 <> ''",
	} {
		if !strings.Contains(documentFileHashUp, fragment) {
			t.Fatalf("document file-hash migration does not include %q", fragment)
		}
	}
}

func TestDocumentProcessingSpanMigrationCreatesLifecycleTable(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS document_processing_spans",
		"REFERENCES documents(id) ON DELETE CASCADE",
		"uq_document_processing_spans_attempt_kind_scope",
		"idx_document_processing_spans_active",
	} {
		if !strings.Contains(documentProcessingSpansUp, fragment) {
			t.Fatalf("processing span migration does not include %q", fragment)
		}
	}
}

func TestDocumentProcessingSpanParentsMigrationAddsCascadeLink(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS parent_span_id UUID NULL",
		"REFERENCES document_processing_spans(id) ON DELETE CASCADE",
		"idx_document_processing_spans_parent_active",
	} {
		if !strings.Contains(documentProcessingSpanParentsUp, fragment) {
			t.Fatalf("processing span parent migration does not include %q", fragment)
		}
	}
}

func TestProcessingRunsMigrationAddsControlPlaneAndLegacyCompatibility(t *testing.T) {
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS processing_runs",
		"uq_processing_runs_idempotency",
		"idx_processing_runs_active_owner_workflow",
		"idx_processing_runs_terminal_retention",
		"ALTER COLUMN attempt TYPE BIGINT",
		"ADD COLUMN IF NOT EXISTS run_id UUID NULL",
		"REFERENCES processing_runs(id) ON DELETE CASCADE",
	} {
		if !strings.Contains(processingRunsUp, fragment) {
			t.Fatalf("processing runs migration does not include %q", fragment)
		}
	}
	for _, fragment := range []string{
		"DROP COLUMN IF EXISTS run_id",
		"DROP TABLE IF EXISTS processing_runs",
		"attempt intentionally remains BIGINT",
	} {
		if !strings.Contains(processingRunsDown, fragment) {
			t.Fatalf("processing runs rollback does not include %q", fragment)
		}
	}
	if got := migrations[len(migrations)-1].version; got != 31 {
		t.Fatalf("latest migration version = %d, want 31", got)
	}
	for _, fragment := range []string{
		"CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public",
		"ALTER EXTENSION pg_trgm SET SCHEMA public",
		"ADD COLUMN IF NOT EXISTS author_names TEXT NOT NULL DEFAULT ''",
		"paper_metadata_sync_author_names",
		"idx_paper_metadata_title_trgm",
		"idx_paper_metadata_author_names_trgm",
		"idx_paper_metadata_updated_at",
		"gin_trgm_ops",
	} {
		if !strings.Contains(paperSearchIndexesUp, fragment) {
			t.Fatalf("paper search migration does not include %q", fragment)
		}
	}
	for _, fragment := range []string{
		"download_attempt UUID",
		"download_requested_at TIMESTAMPTZ",
		"download_error_code TEXT",
		"download_error_message TEXT",
	} {
		if !strings.Contains(referenceDownloadStatusUp, fragment) {
			t.Fatalf("reference download migration does not include %q", fragment)
		}
	}
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS paper_references",
		"resolution_generation BIGINT NOT NULL DEFAULT 0",
		"resolved_document_id UUID REFERENCES documents(id) ON DELETE SET NULL",
		"CREATE TABLE IF NOT EXISTS paper_citations",
		"reference_id UUID NOT NULL REFERENCES paper_references(id) ON DELETE CASCADE",
		"FOREIGN KEY (canonical_document_id) REFERENCES documents(id) ON DELETE CASCADE",
	} {
		if !strings.Contains(paperReferencesUp, fragment) {
			t.Fatalf("paper references migration does not include %q", fragment)
		}
	}
	for _, fragment := range []string{
		"CREATE TABLE paper_citations_next",
		"citing_document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE",
		"reference_key TEXT NOT NULL",
		"cited_document_id UUID REFERENCES documents(id) ON DELETE SET NULL",
		"UNIQUE (citing_document_id, reference_key)",
		"DROP TABLE paper_references",
	} {
		if !strings.Contains(citationReferenceRowsUp, fragment) {
			t.Fatalf("citation reference rows migration does not include %q", fragment)
		}
	}
}

func TestDocumentSourceLifecycleMigrationAddsCanonicalFenceAndBoundedErrors(t *testing.T) {
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS source_state TEXT NOT NULL DEFAULT 'available'",
		"ADD COLUMN IF NOT EXISTS canonical_document_id UUID NULL",
		"REFERENCES documents(id) ON DELETE RESTRICT",
		"source_state IN ('missing', 'available', 'deleting', 'merged')",
		"source_state = 'merged' AND canonical_document_id IS NOT NULL",
		"length(COALESCE(reference_lookup_error_message, '')) <= 512",
		"idx_documents_canonical_document_id",
	} {
		if !strings.Contains(documentSourceLifecycleUp, fragment) {
			t.Fatalf("document source lifecycle migration does not include %q", fragment)
		}
	}
	for _, fragment := range []string{
		"DROP CONSTRAINT IF EXISTS fk_documents_canonical_document",
		"DROP COLUMN IF EXISTS source_state",
		"DROP COLUMN IF EXISTS canonical_document_id",
	} {
		if !strings.Contains(documentSourceLifecycleDown, fragment) {
			t.Fatalf("document source lifecycle rollback does not include %q", fragment)
		}
	}
}
