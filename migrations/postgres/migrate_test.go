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
		"CREATE TABLE IF NOT EXISTS document_bases",
		"CREATE TABLE IF NOT EXISTS document_base_files",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(paperMetadataAndBasesUp, fragment) {
			t.Fatalf("paper metadata/base migration does not include %q", fragment)
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
		"markdown_sha256 VARCHAR(64)",
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
