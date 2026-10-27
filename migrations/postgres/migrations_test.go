package postgres

import (
	"github.com/google/uuid"
	driver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"net/url"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Consolidated from migrate_test.go.
// ---------------------------------------------------------------------------

// TestDocumentsMigrationContainsProjectTables verifies that the base schema
// creates documents and document_blocks without retired tables, and requires URLs
// for image and table blocks.
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

// TestHardeningMigrationAddsDatabaseGuards verifies that hardening adds the
// dead-letter table, nonempty DOI uniqueness, a dead-letter scope/cursor index,
// and the deleting source state.
func TestHardeningMigrationAddsDatabaseGuards(t *testing.T) {
	for _, fragment := range []string{"CREATE TABLE IF NOT EXISTS task_dead_letters", "uq_documents_doi_nonempty", "idx_task_dead_letters_scope_cursor", "'deleting'"} {
		if !strings.Contains(documentHardeningUp, fragment) {
			t.Fatalf("hardening migration does not include %q", fragment)
		}
	}
}

// TestCancelledMigrationAddsCancelledStatus verifies that the cancellation
// migration extends the document status constraint with the cancelled value.
func TestCancelledMigrationAddsCancelledStatus(t *testing.T) {
	if !strings.Contains(documentCancelledUp, "'cancelled'") {
		t.Fatal("cancelled migration does not add the cancelled document status")
	}
}

// TestProcessingRunItemsMigrationSeparatesFanoutControlAndIndexesView verifies
// that the fanout migration adds run items with item sealing plus indexes for
// active items, the current-span view, and owner views.
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

// TestDocumentStageControlMigrationAddsFourIndependentStages verifies that the
// stage-control migration adds independent status/generation columns, active
// indexes, and backfills extraction provenance and reference-run scope.
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

// TestProcessingSpanRetentionMigrationUsesTerminalAndParentIndexes verifies that
// retention adds indexes for terminal-time pruning and active parent lookup with
// deterministic ordering.
func TestProcessingSpanRetentionMigrationUsesTerminalAndParentIndexes(t *testing.T) {
	for _, fragment := range []string{"idx_document_processing_spans_terminal_retention", "finished_at ASC, id ASC", "idx_document_processing_spans_parent_active"} {
		if !strings.Contains(processingSpanRetentionUp, fragment) {
			t.Fatalf("processing span retention migration does not include %q", fragment)
		}
	}
}

// TestLegacyProcessingControlRemovalIsExplicitAndIrreversible verifies that
// legacy processing columns and tables are explicitly dropped and that the
// rollback is documented as intentionally irreversible.
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

// TestPaperMetadataAndBasesMigrationCreatesNormalizedTables verifies that the
// metadata/base migration renames the legacy filename, creates normalized tables,
// defaults tags and provenance, and cascades memberships.
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

// TestPaperMetadataSourceMigrationAddsOriginPrecedenceField verifies that the
// provenance migration adds metadata_source with a constraint allowing only the
// expected origin values.
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

// TestPaperMetadataFieldSourcesMigrationProtectsHistoricalManualEdits verifies
// that field_sources is added as JSONB and historical manual title edits are
// backfilled for rows with no prior field provenance.
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

// TestDocumentBaseFileRenameMigrationSupportsExistingDeployments verifies that
// existing deployments have document_base_documents renamed to
// document_base_files.
func TestDocumentBaseFileRenameMigrationSupportsExistingDeployments(t *testing.T) {
	for _, fragment := range []string{"document_base_documents", "document_base_files", "RENAME TO document_base_files"} {
		if !strings.Contains(documentBaseFilesRenameUp, fragment) {
			t.Fatalf("document base file rename migration does not include %q", fragment)
		}
	}
}

// TestPaperMetadataTagsIndexMigration verifies that tag search is backed by a
// GIN jsonb_path_ops index on the metadata tags column.
func TestPaperMetadataTagsIndexMigration(t *testing.T) {
	if !strings.Contains(paperMetadataTagsIndexUp, "USING GIN (tags jsonb_path_ops)") {
		t.Fatal("paper metadata tag migration does not create a GIN JSONB index")
	}
}

// TestDocumentMarkdownMigrationCreatesDurableSourceManifest verifies that
// document_markdowns is a one-to-one cascading manifest with a unique storage
// key, page count, and no obsolete asset counter.
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

// TestDocumentBlockSourceMapMigrationAddsTypedPDFLocation verifies that block
// migration adds typed PDF page/bbox and Markdown code-point columns plus a page
// index.
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

// TestRemoveMarkdownSHA256MigrationDropsDerivedRevisionColumns verifies that the
// cleanup migration removes the derived Markdown and block SHA-256 columns.
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

// TestDocumentBlockAssetMigrationKeepsStorageKeyPrivate verifies that derived
// block images are referenced through a private asset_key column.
func TestDocumentBlockAssetMigrationKeepsStorageKeyPrivate(t *testing.T) {
	if !strings.Contains(documentBlockAssetsUp, "asset_key TEXT NOT NULL DEFAULT ''") {
		t.Fatal("document block asset migration does not add asset_key")
	}
}

// TestBibliographyOrderMigrationAddsNullableOrderedIndex verifies that citation
// bibliography order accepts NULL or a nonnegative value, is indexed, and is
// dropped cleanly on rollback.
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

// TestDocumentOutlineMigrationAddsExplicitGenerationBoundManifest verifies that
// the outline migration adds a JSONB manifest with a legacy backfill marker, a
// recognition generation, and an object-shape constraint.
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

// TestDocumentReadingNotesMigrationCreatesBoundedRevisionedNote verifies that
// reading notes store a revisioned JSON manifest with a bounded size and cascade
// deletion instead of monolithic Markdown or a single storage key.
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

// TestDocumentReadingNoteLimitsMigrationAllowsEmptyAndSixtyMiB verifies that the
// replaced size constraint admits empty notes and content up to 60 MiB.
func TestDocumentReadingNoteLimitsMigrationAllowsEmptyAndSixtyMiB(t *testing.T) {
	for _, fragment := range []string{
		"DROP CONSTRAINT IF EXISTS chk_document_reading_notes_size",
		"size_bytes >= 0 AND size_bytes <= 62914560",
	} {
		if !strings.Contains(documentReadingNoteLimitsUp, fragment) {
			t.Fatalf("document reading note limits migration does not include %q", fragment)
		}
	}
}

// TestPaperCitationsMigrationCreatesDirectionalCascadeRelation verifies that
// paper_citations uses directional citing/cited cascade keys, a composite
// primary key, a self-reference check, and a cited-document index.
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

// TestPaperCitationOriginsMigrationProtectsHistoricalEdges verifies that
// citation origin defaults to manual, is constrained to manual or extraction,
// and is indexed by automatic owner.
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

// TestUnknownCitationsMigrationAddsPlaceholderStatusAndRawEvidence verifies that
// unresolved historical citations receive an unknown status and a raw evidence
// column before the temporary default is removed.
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

// TestDocumentFileHashMigrationAddsDeduplicationGuard verifies that the file-hash
// migration adds a partial unique index that prevents duplicate nonempty SHA-256
// uploads.
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

// TestDocumentProcessingSpanMigrationCreatesLifecycleTable verifies that
// processing spans get a cascading lifecycle table with uniqueness per
// attempt/kind/scope and an active-span index.
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

// TestDocumentProcessingSpanParentsMigrationAddsCascadeLink verifies that spans
// gain a self-referencing parent link with cascade deletion and an active-parent
// index.
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

// TestProcessingRunsMigrationAddsControlPlaneAndLegacyCompatibility verifies
// that the latest migration set adds idempotent processing runs, run-scoped
// spans, search indexes, reference download fields, and the citation-row table.
// It also checks that rollback keeps the widened attempt type and that version 33
// is the newest migration.
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
	if got := migrations[len(migrations)-1].version; got != 33 {
		t.Fatalf("latest migration version = %d, want 33", got)
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

// TestDocumentSourceLifecycleMigrationAddsCanonicalFenceAndBoundedErrors verifies
// that source lifecycle adds constrained states, canonical links with a
// non-cascading reference, bounded error messages, an index, and a clean rollback.
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

// ---------------------------------------------------------------------------
// Consolidated from processing_runs_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestProcessingRunsMigrationUpgradeAndRollbackPostgres verifies against
// PostgreSQL that applying the run migrations widens attempt to bigint and that
// rollback removes the run and item tables while intentionally retaining the
// widened type.
func TestProcessingRunsMigrationUpgradeAndRollbackPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(driver.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("get admin sql.DB: %v", err)
	}
	defer adminSQL.Close()
	schema := "processing_run_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(driver.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get scoped sql.DB: %v", err)
	}
	defer sqlDB.Close()
	if err := db.Exec(`CREATE TABLE documents (id UUID PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create documents: %v", err)
	}
	if err := db.Exec(`CREATE TABLE document_processing_spans (
		id UUID PRIMARY KEY,
		document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
		attempt INTEGER NOT NULL CHECK (attempt > 0),
		kind TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT 'document',
		scope_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create legacy spans: %v", err)
	}
	if err := db.Exec(processingRunsUp).Error; err != nil {
		t.Fatalf("apply processing runs migration: %v", err)
	}
	if err := db.Exec(processingRunItemsAndSpanViewUp).Error; err != nil {
		t.Fatalf("apply processing run items migration: %v", err)
	}
	var dataType string
	if err := db.Raw(`SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_processing_spans' AND column_name = 'attempt'`).Scan(&dataType).Error; err != nil {
		t.Fatalf("inspect widened attempt: %v", err)
	}
	if dataType != "bigint" {
		t.Fatalf("attempt data type = %q, want bigint", dataType)
	}
	if err := db.Exec(processingRunItemsAndSpanViewDown).Error; err != nil {
		t.Fatalf("rollback processing run items migration: %v", err)
	}
	var itemTable *string
	if err := db.Raw("SELECT to_regclass('processing_run_items')").Scan(&itemTable).Error; err != nil {
		t.Fatalf("inspect rolled back run item table: %v", err)
	}
	if itemTable != nil {
		t.Fatalf("processing_run_items still exists after rollback: %q", *itemTable)
	}
	if err := db.Exec(processingRunsDown).Error; err != nil {
		t.Fatalf("rollback processing runs migration: %v", err)
	}
	var runTable *string
	if err := db.Raw("SELECT to_regclass('processing_runs')").Scan(&runTable).Error; err != nil {
		t.Fatalf("inspect rolled back table: %v", err)
	}
	if runTable != nil {
		t.Fatalf("processing_runs still exists after rollback: %q", *runTable)
	}
	dataType = ""
	if err := db.Raw(`SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'document_processing_spans' AND column_name = 'attempt'`).Scan(&dataType).Error; err != nil {
		t.Fatalf("inspect rollback attempt: %v", err)
	}
	if dataType != "bigint" {
		t.Fatalf("rollback narrowed attempt to %q, want bigint", dataType)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_stage_control_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestDocumentStageControlMigrationUpgradeBackfillAndRollbackPostgres verifies
// against PostgreSQL that stage control backfills artifact and active
// statuses/generations from legacy rows, leaves plain ready documents untouched,
// enforces the status constraint and four indexes, and removes the columns on
// rollback.
func TestDocumentStageControlMigrationUpgradeBackfillAndRollbackPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(driver.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("get admin sql.DB: %v", err)
	}
	defer adminSQL.Close()
	schema := "document_stage_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	}()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(driver.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get scoped sql.DB: %v", err)
	}
	defer sqlDB.Close()

	for _, migration := range migrations[:20] {
		if err := db.Exec(migration.up).Error; err != nil {
			t.Fatalf("apply prerequisite migration %d: %v", migration.version, err)
		}
	}
	artifactID, activeID, legacyReadyID := uuid.New(), uuid.New(), uuid.New()
	for id, status := range map[uuid.UUID]string{
		artifactID: "ready", activeID: "processing", legacyReadyID: "ready",
	} {
		if err := db.Exec(`INSERT INTO documents
			(id, original_filename, content_type, size_bytes, storage_key, status)
			VALUES (?, 'legacy.pdf', 'application/pdf', 1, ?, ?)`, id, "legacy/"+id.String(), status).Error; err != nil {
			t.Fatalf("insert legacy document: %v", err)
		}
		if err := db.Exec("INSERT INTO paper_metadata (document_id) VALUES (?)", id).Error; err != nil {
			t.Fatalf("insert legacy metadata: %v", err)
		}
	}
	if err := db.Exec(`INSERT INTO document_markdowns
		(document_id, storage_key, filename, size_bytes, updated_at)
		VALUES (?, ?, 'artifact.md', 10, '2026-01-02 03:04:05+00')`, artifactID, "markdown/"+artifactID.String()).Error; err != nil {
		t.Fatalf("insert Markdown artifact: %v", err)
	}
	if err := db.Exec(`UPDATE paper_metadata SET metadata_source = 'extraction',
		updated_at = '2026-01-02 03:05:06+00' WHERE document_id = ?`, artifactID).Error; err != nil {
		t.Fatalf("mark extraction artifact: %v", err)
	}
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, attempt, kind, scope, scope_id, status, started_at, updated_at)
		VALUES
		(?, ?, 7, 'recognition', 'document', 'recognition:active', 'running', CURRENT_TIMESTAMP, '2026-02-01 01:00:00+00'),
		(?, ?, 9, 'catalog_source', 'source', 'catalog_source:active', 'queued', CURRENT_TIMESTAMP, '2026-02-01 02:00:00+00')`,
		uuid.New(), activeID, uuid.New(), activeID).Error; err != nil {
		t.Fatalf("insert active spans: %v", err)
	}
	if err := db.Exec(`INSERT INTO processing_runs
		(id, owner_document_id, workflow_kind, idempotency_key, attempt, state, updated_at)
		VALUES (?, ?, 'catalog_reference', 'reference:active', 11, 'queued', '2026-02-01 03:00:00+00')`,
		uuid.New(), activeID).Error; err != nil {
		t.Fatalf("insert active reference run: %v", err)
	}

	if err := db.Exec(documentStageControlUp).Error; err != nil {
		t.Fatalf("apply document stage migration: %v", err)
	}
	type stageRow struct {
		MarkdownStatus               string
		MarkdownGeneration           int64
		MetadataExtractionStatus     string
		MetadataExtractionGeneration int64
		SourceLookupStatus           string
		SourceLookupGeneration       int64
		ReferenceLookupStatus        string
		ReferenceLookupGeneration    int64
	}
	load := func(id uuid.UUID) stageRow {
		t.Helper()
		var row stageRow
		if err := db.Table("documents").Where("id = ?", id).Take(&row).Error; err != nil {
			t.Fatalf("load stage row %s: %v", id, err)
		}
		return row
	}
	artifact := load(artifactID)
	if artifact.MarkdownStatus != "succeeded" || artifact.MarkdownGeneration != 1 ||
		artifact.MetadataExtractionStatus != "succeeded" || artifact.MetadataExtractionGeneration != 1 {
		t.Fatalf("artifact backfill = %#v", artifact)
	}
	active := load(activeID)
	if active.MarkdownStatus != "running" || active.MarkdownGeneration != 7 ||
		active.SourceLookupStatus != "queued" || active.SourceLookupGeneration != 9 ||
		active.ReferenceLookupStatus != "queued" || active.ReferenceLookupGeneration != 11 {
		t.Fatalf("active backfill = %#v", active)
	}
	legacyReady := load(legacyReadyID)
	if legacyReady.MarkdownStatus != "not_started" || legacyReady.MetadataExtractionStatus != "not_started" ||
		legacyReady.SourceLookupStatus != "not_started" || legacyReady.ReferenceLookupStatus != "not_started" {
		t.Fatalf("legacy ready row was overclaimed: %#v", legacyReady)
	}
	if err := db.Exec("UPDATE documents SET markdown_status = 'invalid' WHERE id = ?", artifactID).Error; err == nil {
		t.Fatal("invalid stage status should violate the check constraint")
	}
	var indexCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname IN ('idx_documents_markdown_stage_active',
		'idx_documents_metadata_extraction_stage_active',
		'idx_documents_source_lookup_stage_active',
		'idx_documents_reference_lookup_stage_active')`).Scan(&indexCount).Error; err != nil {
		t.Fatalf("inspect stage indexes: %v", err)
	}
	if indexCount != 4 {
		t.Fatalf("stage index count = %d, want 4", indexCount)
	}

	if err := db.Exec(documentStageControlDown).Error; err != nil {
		t.Fatalf("rollback document stage migration: %v", err)
	}
	var columnCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'documents'
		AND column_name IN ('markdown_status', 'metadata_extraction_status',
		'source_lookup_status', 'reference_lookup_status')`).Scan(&columnCount).Error; err != nil {
		t.Fatalf("inspect rolled back columns: %v", err)
	}
	if columnCount != 0 {
		t.Fatalf("stage status columns remaining after rollback = %d", columnCount)
	}
}
