// Package postgres applies versioned PostgreSQL schema migrations embedded in
// the API binary.
package postgres

import (
	"context"
	_ "embed"
	"fmt"

	"gorm.io/gorm"
)

const migrationLockID int64 = 2026072101

//go:embed 000001_documents.up.sql
var documentsUp string

//go:embed 000002_document_hardening.up.sql
var documentHardeningUp string

//go:embed 000003_document_cancelled.up.sql
var documentCancelledUp string

//go:embed 000004_paper_metadata_and_bases.up.sql
var paperMetadataAndBasesUp string

//go:embed 000005_rename_document_base_files.up.sql
var documentBaseFilesRenameUp string

//go:embed 000006_paper_metadata_tags_index.up.sql
var paperMetadataTagsIndexUp string

//go:embed 000007_document_markdowns.up.sql
var documentMarkdownsUp string

//go:embed 000008_document_block_source_map.up.sql
var documentBlockSourceMapUp string

//go:embed 000009_document_block_assets.up.sql
var documentBlockAssetsUp string

//go:embed 000010_paper_citations.up.sql
var paperCitationsUp string

//go:embed 000011_unknown_citations.up.sql
var unknownCitationsUp string

//go:embed 000012_paper_metadata_source.up.sql
var paperMetadataSourceUp string

//go:embed 000013_document_file_hash.up.sql
var documentFileHashUp string

//go:embed 000014_document_processing_spans.up.sql
var documentProcessingSpansUp string

//go:embed 000015_document_processing_span_parents.up.sql
var documentProcessingSpanParentsUp string

//go:embed 000016_remove_markdown_sha256.up.sql
var removeMarkdownSHA256Up string

//go:embed 000017_paper_metadata_field_sources.up.sql
var paperMetadataFieldSourcesUp string

//go:embed 000018_paper_citation_origins.up.sql
var paperCitationOriginsUp string

//go:embed 000019_processing_runs.up.sql
var processingRunsUp string

//go:embed 000019_processing_runs.down.sql
var processingRunsDown string

//go:embed 000020_processing_run_items_and_span_view.up.sql
var processingRunItemsAndSpanViewUp string

//go:embed 000020_processing_run_items_and_span_view.down.sql
var processingRunItemsAndSpanViewDown string

//go:embed 000021_document_stage_control.up.sql
var documentStageControlUp string

//go:embed 000021_document_stage_control.down.sql
var documentStageControlDown string

//go:embed 000022_document_source_lifecycle.up.sql
var documentSourceLifecycleUp string

//go:embed 000022_document_source_lifecycle.down.sql
var documentSourceLifecycleDown string

//go:embed 000023_processing_span_retention.up.sql
var processingSpanRetentionUp string

//go:embed 000023_processing_span_retention.down.sql
var processingSpanRetentionDown string

//go:embed 000024_remove_legacy_processing_control.up.sql
var removeLegacyProcessingControlUp string

//go:embed 000024_remove_legacy_processing_control.down.sql
var removeLegacyProcessingControlDown string

//go:embed 000025_paper_references.up.sql
var paperReferencesUp string

//go:embed 000025_paper_references.down.sql
var paperReferencesDown string

//go:embed 000026_citation_reference_rows.up.sql
var citationReferenceRowsUp string

//go:embed 000026_citation_reference_rows.down.sql
var citationReferenceRowsDown string

//go:embed 000027_reference_download_status.up.sql
var referenceDownloadStatusUp string

//go:embed 000027_reference_download_status.down.sql
var referenceDownloadStatusDown string

//go:embed 000028_paper_search_indexes.up.sql
var paperSearchIndexesUp string

//go:embed 000028_paper_search_indexes.down.sql
var paperSearchIndexesDown string

//go:embed 000029_bibliography_order.up.sql
var bibliographyOrderUp string

//go:embed 000029_bibliography_order.down.sql
var bibliographyOrderDown string

//go:embed 000030_document_outline.up.sql
var documentOutlineUp string

//go:embed 000030_document_outline.down.sql
var documentOutlineDown string

//go:embed 000031_document_reading_notes.up.sql
var documentReadingNotesUp string

//go:embed 000031_document_reading_notes.down.sql
var documentReadingNotesDown string

//go:embed 000032_document_reading_note_limits.up.sql
var documentReadingNoteLimitsUp string

//go:embed 000032_document_reading_note_limits.down.sql
var documentReadingNoteLimitsDown string

//go:embed 000033_document_source_lookup_mode.up.sql
var documentSourceLookupModeUp string

//go:embed 000033_document_source_lookup_mode.down.sql
var documentSourceLookupModeDown string

type migration struct {
	version int64
	up      string
}

// 手动指定版本啊
var migrations = []migration{
	{version: 1, up: documentsUp},
	{version: 2, up: documentHardeningUp},
	{version: 3, up: documentCancelledUp},
	{version: 4, up: paperMetadataAndBasesUp},
	{version: 5, up: documentBaseFilesRenameUp},
	{version: 6, up: paperMetadataTagsIndexUp},
	{version: 7, up: documentMarkdownsUp},
	{version: 8, up: documentBlockSourceMapUp},
	{version: 9, up: documentBlockAssetsUp},
	{version: 10, up: paperCitationsUp},
	{version: 11, up: unknownCitationsUp},
	{version: 12, up: paperMetadataSourceUp},
	{version: 13, up: documentFileHashUp},
	{version: 14, up: documentProcessingSpansUp},
	{version: 15, up: documentProcessingSpanParentsUp},
	{version: 16, up: removeMarkdownSHA256Up},
	{version: 17, up: paperMetadataFieldSourcesUp},
	{version: 18, up: paperCitationOriginsUp},
	{version: 19, up: processingRunsUp},
	{version: 20, up: processingRunItemsAndSpanViewUp},
	{version: 21, up: documentStageControlUp},
	{version: 22, up: documentSourceLifecycleUp},
	{version: 23, up: processingSpanRetentionUp},
	{version: 24, up: removeLegacyProcessingControlUp},
	{version: 25, up: paperReferencesUp},
	{version: 26, up: citationReferenceRowsUp},
	{version: 27, up: referenceDownloadStatusUp},
	{version: 28, up: paperSearchIndexesUp},
	{version: 29, up: bibliographyOrderUp},
	{version: 30, up: documentOutlineUp},
	{version: 31, up: documentReadingNotesUp},
	{version: 32, up: documentReadingNoteLimitsUp},
	{version: 33, up: documentSourceLookupModeUp},
}

// Migrate applies every unapplied PostgreSQL migration exactly once. The
// advisory lock prevents multiple API instances from applying the same version
// concurrently during a rolling deployment.
func Migrate(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationLockID).Error; err != nil {
			return fmt.Errorf("lock migrations: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`).Error; err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}

		for _, migration := range migrations {
			var applied int64
			if err := tx.Table("schema_migrations").Where("version = ?", migration.version).Count(&applied).Error; err != nil {
				return fmt.Errorf("check migration %d: %w", migration.version, err)
			}
			if applied != 0 {
				continue
			}
			if err := tx.Exec(migration.up).Error; err != nil {
				return fmt.Errorf("apply migration %d: %w", migration.version, err)
			}
			if err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", migration.version).Error; err != nil {
				return fmt.Errorf("record migration %d: %w", migration.version, err)
			}
		}
		return nil
	})
}
