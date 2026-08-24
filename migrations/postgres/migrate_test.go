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
