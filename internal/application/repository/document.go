// Package repository contains database operations for application tables.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	postgresmigrations "paperMaster/migrations/postgres"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type documentRepository struct {
	db *gorm.DB
}

// NewDocumentRepository creates the document-table repository with the GORM
// connection configured by NewDatabase.
func NewDocumentRepository(db *gorm.DB) interfaces.DocumentRepository {
	return &documentRepository{db: db}
}

// Migrate applies the versioned PostgreSQL schema that backs the GORM models.
func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := postgresmigrations.Migrate(ctx, db); err != nil {
		logger.Error(ctx, "migrate document tables failed", "error", err)
		return err
	}
	return nil
}

func (r *documentRepository) Create(ctx context.Context, document types.Document) (types.Document, error) {
	document.DOI = strings.ToLower(strings.TrimSpace(document.DOI))
	document.FileSHA256 = strings.ToLower(strings.TrimSpace(document.FileSHA256))
	err := r.db.WithContext(ctx).Create(&document).Error
	if isDuplicateDOIError(err) {
		err = fmt.Errorf("%w: %s", types.ErrDuplicateDOI, document.DOI)
	}
	if isDuplicateFileHashError(err) {
		err = fmt.Errorf("%w: %s", types.ErrDuplicateFileHash, document.FileSHA256)
	}
	if err != nil {
		logger.Error(ctx, "create document record failed", "document_id", document.ID, "storage_key", document.StorageKey, "error", err)
	} else {
		logger.Info(ctx, "document record created", "document_id", document.ID, "storage_key", document.StorageKey)
	}
	if err != nil {
		return document, err
	}
	return document, nil
}

func (r *documentRepository) Get(ctx context.Context, id uuid.UUID) (types.Document, error) {
	var document types.Document
	err := r.db.WithContext(ctx).First(&document, "id = ?", id).Error
	err = repositoryError(err)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document record failed", "document_id", id, "error", err)
	}
	return document, err
}

func (r *documentRepository) Search(ctx context.Context, search types.DocumentSearch) (types.DocumentPage, error) {
	page := search.Page
	if page < 1 {
		page = 1
	}
	if page > types.MaxDocumentSearchPage {
		return types.DocumentPage{}, fmt.Errorf("page exceeds maximum %d", types.MaxDocumentSearchPage)
	}
	pageSize := search.PageSize
	if pageSize <= 0 {
		pageSize = types.DocumentPageSize
	}

	query := r.db.WithContext(ctx).Model(&types.Document{})
	if search.ID != nil {
		query = query.Where("documents.id = ?", *search.ID)
	}
	if search.DOI != "" {
		query = query.Where("documents.doi = ?", search.DOI)
	}
	if search.FileSHA256 != "" {
		query = query.Where("documents.file_sha256 = ?", strings.ToLower(strings.TrimSpace(search.FileSHA256)))
	}
	if search.SourceState != "" {
		query = query.Where("documents.source_state = ?", search.SourceState)
	}
	if len(search.ExcludeIDs) > 0 {
		query = query.Where("documents.id NOT IN ?", search.ExcludeIDs)
	}
	if search.ExcludeUnknown {
		query = query.Where("documents.source_state = ?", types.DocumentSourceAvailable)
	}

	result := types.DocumentPage{Page: page, PageSize: pageSize, Items: []types.Document{}}
	if err := query.Count(&result.Total).Error; err != nil {
		logger.Error(ctx, "count document records failed", "page", page, "error", err)
		return result, err
	}
	err := query.Order("documents.created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&result.Items).Error
	if err != nil {
		logger.Error(ctx, "search document records failed", "page", page, "error", err)
	}
	return result, err
}

func (r *documentRepository) CitationTargetProgress(ctx context.Context, citingDocumentID uuid.UUID) (types.PaperReferenceProgress, error) {
	var progress types.PaperReferenceProgress
	err := r.db.WithContext(ctx).Raw(`SELECT
		COUNT(*) FILTER (WHERE resolution_status IN ('not_started','queued')) AS queued,
		COUNT(*) FILTER (WHERE resolution_status = 'running') AS running,
		COUNT(*) FILTER (WHERE resolution_status IN ('succeeded','skipped') OR cited_document_id IS NOT NULL) AS completed,
		COUNT(*) FILTER (WHERE resolution_status = 'failed') AS failed,
		COUNT(*) FILTER (WHERE resolution_status = 'cancelled') AS cancelled
	FROM paper_citations
	WHERE citing_document_id = ?`, citingDocumentID).Scan(&progress).Error
	if err != nil {
		logger.Error(ctx, "load reference resolution progress failed", "document_id", citingDocumentID, "error", err)
	}
	return progress, err
}

func (r *documentRepository) GetMarkdown(ctx context.Context, documentID uuid.UUID) (types.DocumentMarkdown, error) {
	var markdown types.DocumentMarkdown
	err := repositoryError(r.db.WithContext(ctx).First(&markdown, "document_id = ?", documentID).Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document markdown failed", "document_id", documentID, "error", err)
	}
	return markdown, err
}

func (r *documentRepository) ReplaceParsedOutput(
	ctx context.Context, markdown types.DocumentMarkdown, blocks []types.Block, markdownGeneration int64,
) error {
	if markdown.DocumentID == uuid.Nil {
		return errors.New("markdown document id is required")
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&types.Document{}).Where("id = ? AND source_state = ?", markdown.DocumentID, types.DocumentSourceAvailable)
		updates := map[string]interface{}{
			"markdown_status":        types.DocumentStageSucceeded,
			"markdown_updated_at":    time.Now().UTC(),
			"markdown_error_code":    nil,
			"markdown_error_message": nil,
		}
		if markdownGeneration > 0 {
			query = query.Where("markdown_status = ? AND markdown_generation = ?", types.DocumentStageRunning, markdownGeneration)
		} else {
			return types.ErrDocumentNotProcessing
		}
		result := query.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return types.ErrDocumentNotProcessing
		}

		if err := tx.Where("document_id = ?", markdown.DocumentID).Delete(&types.Block{}).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "document_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"storage_key", "filename", "content_type", "size_bytes", "parser_name", "parser_version", "input_mode", "language", "page_count", "metadata", "updated_at"}),
		}).Create(&markdown).Error; err != nil {
			return err
		}
		if len(blocks) == 0 {
			return nil
		}
		return tx.Create(&blocks).Error
	})
	if err != nil {
		logger.Error(ctx, "replace document parsed output failed", "document_id", markdown.DocumentID, "block_count", len(blocks), "error", err)
		return err
	}
	logger.Info(ctx, "document parsed output persisted", "document_id", markdown.DocumentID, "block_count", len(blocks))
	return nil
}

func (r *documentRepository) ListBlocks(ctx context.Context, documentID uuid.UUID, pageNumber int) ([]types.Block, error) {
	blocks := make([]types.Block, 0)
	query := r.db.WithContext(ctx).Where("document_id = ?", documentID)
	if pageNumber > 0 {
		query = query.Where("page_number = ?", pageNumber)
	}
	err := query.Order("page_number ASC, order_index ASC").Find(&blocks).Error
	if err != nil {
		logger.Error(ctx, "list document blocks failed", "document_id", documentID, "page_number", pageNumber, "error", err)
		return nil, err
	}
	return blocks, nil
}

func (r *documentRepository) GetBlock(ctx context.Context, documentID, blockID uuid.UUID) (types.Block, error) {
	var block types.Block
	err := repositoryError(r.db.WithContext(ctx).First(&block, "document_id = ? AND id = ?", documentID, blockID).Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document block failed", "document_id", documentID, "block_id", blockID, "error", err)
	}
	return block, err
}

func (r *documentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	var deadLettersDeleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// task_dead_letters intentionally has no foreign key: it also records
		// generic queue failures. Document-scoped diagnostic rows therefore
		// remain the one non-cascading relation removed here.
		documentIDText := id.String()
		deadLetters := tx.Where("(scope = ? AND scope_id = ?) OR related_id = ?", types.TaskScopeDocument, documentIDText, documentIDText).
			Delete(&types.TaskDeadLetter{})
		if deadLetters.Error != nil {
			return deadLetters.Error
		}
		deadLettersDeleted = deadLetters.RowsAffected

		// Bibliography rows that pointed at this document become unresolved
		// again. Rows owned by the deleted document cascade with it.
		now := time.Now().UTC()
		if err := tx.Exec(`UPDATE paper_citations
			SET cited_document_id = NULL,
			    resolution_status = 'not_started',
			    resolution_generation = resolution_generation + 1,
			    resolution_provider = '', resolution_error_code = NULL,
			    resolution_error_message = NULL, resolution_updated_at = ?, updated_at = ?
			WHERE cited_document_id = ?`, now, now, id).Error; err != nil {
			return err
		}

		result := tx.Delete(&types.Document{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error(ctx, "delete document database record failed", "document_id", id, "error", err)
		}
		return err
	}
	logger.Info(ctx, "document database record deleted", "document_id", id, "dead_letters_deleted", deadLettersDeleted,
		"cascaded_relations", "document_blocks,document_markdowns,paper_metadata,document_base_files,paper_citations")
	return nil
}

func (r *documentRepository) MarkDeleting(ctx context.Context, id uuid.UUID) (types.DocumentSourceState, bool, error) {
	var document types.Document
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&document, "id = ?", id).Error; err != nil {
			return err
		}
		if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
			return nil
		}
		result := tx.Model(&types.Document{}).Where("id = ? AND source_state = ?", id, document.SourceState).
			Update("source_state", types.DocumentSourceDeleting)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrDocumentNotProcessing
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, sql.ErrNoRows
	}
	if err != nil {
		logger.Error(ctx, "mark document deleting failed", "document_id", id, "error", err)
		return "", false, err
	}
	return document.SourceState, document.SourceState != types.DocumentSourceDeleting && document.SourceState != types.DocumentSourceMerged, nil
}

func (r *documentRepository) RestoreSourceState(ctx context.Context, id uuid.UUID, state types.DocumentSourceState) error {
	if state != types.DocumentSourceAvailable {
		return nil
	}
	result := r.db.WithContext(ctx).Model(&types.Document{}).
		Where("id = ? AND source_state = ?", id, types.DocumentSourceDeleting).
		Update("source_state", state)
	if result.Error != nil {
		logger.Error(ctx, "restore document source state failed", "document_id", id, "error", result.Error)
	}
	return result.Error
}

func (r *documentRepository) UpdateDocumentInfo(
	ctx context.Context, id uuid.UUID, originalFilename, doi *string,
) (types.Document, error) {
	var updated types.Document
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentChanges := map[string]interface{}{}
		if originalFilename != nil {
			documentChanges["original_filename"] = *originalFilename
		}
		if doi != nil {
			documentChanges["doi"] = *doi
		}
		if len(documentChanges) > 0 {
			result := tx.Model(&types.Document{}).Where("id = ?", id).Updates(documentChanges)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return sql.ErrNoRows
			}
		}

		return tx.First(&updated, "id = ?", id).Error
	})
	if isDuplicateDOIError(err) {
		err = fmt.Errorf("%w: %s", types.ErrDuplicateDOI, valueOrEmpty(doi))
	}
	if err != nil {
		logger.Error(ctx, "update document information failed", "document_id", id, "error", err)
		return types.Document{}, repositoryError(err)
	}
	logger.Info(ctx, "document information updated", "document_id", id)
	return updated, nil
}

func (r *documentRepository) SetDOIIfEmpty(ctx context.Context, id uuid.UUID, doi string) (bool, error) {
	doi = strings.ToLower(strings.TrimSpace(doi))
	if doi == "" {
		return false, nil
	}
	result := r.db.WithContext(ctx).Model(&types.Document{}).
		Where("id = ? AND doi = ''", id).
		Update("doi", doi)
	if isDuplicateDOIError(result.Error) {
		return false, fmt.Errorf("%w: %s", types.ErrDuplicateDOI, doi)
	}
	if result.Error != nil {
		logger.Error(ctx, "backfill document DOI failed", "document_id", id, "error", result.Error)
		return false, repositoryError(result.Error)
	}
	if result.RowsAffected > 0 {
		logger.Info(ctx, "document DOI backfilled", "document_id", id, "doi", doi)
		return true, nil
	}
	return false, nil
}

// ReplaceOlderDocument implements newest-wins duplicate handling. The older
// document and every merged tombstone under it are removed; its incoming
// citations and collection membership move to the newest document, and the
// newest takes over the DOI. The older document's outgoing bibliography is not
// carried over: the newest upload's own extraction is authoritative.
func (r *documentRepository) ReplaceOlderDocument(
	ctx context.Context, olderID, newestID uuid.UUID,
) (types.DocumentFileKeys, error) {
	cleanup := types.DocumentFileKeys{}
	if olderID == uuid.Nil || newestID == uuid.Nil || olderID == newestID {
		return cleanup, errors.New("distinct older and newest document ids are required")
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ids := []uuid.UUID{olderID, newestID}
		if ids[0].String() > ids[1].String() {
			ids[0], ids[1] = ids[1], ids[0]
		}
		var locked []types.Document
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ?", ids).Order("id").Find(&locked).Error; err != nil {
			return err
		}
		if len(locked) != 2 {
			return sql.ErrNoRows
		}
		var older, newest types.Document
		for _, document := range locked {
			if document.ID == olderID {
				older = document
			} else {
				newest = document
			}
		}
		if older.SourceState != types.DocumentSourceAvailable {
			return fmt.Errorf("older document source is not available")
		}
		if newest.SourceState != types.DocumentSourceAvailable {
			return fmt.Errorf("newest document source is not available")
		}
		// Never replace the only document that owns a stored file with one that
		// has no source. Normally both rows are available and non-empty is
		// enforced by the schema; this is a defensive guard for legacy data or
		// a future index-only document state.
		if strings.TrimSpace(older.StorageKey) != "" && strings.TrimSpace(newest.StorageKey) == "" {
			return fmt.Errorf("cannot replace a sourced document with a source-less document")
		}
		olderDOI := strings.ToLower(strings.TrimSpace(older.DOI))
		newestDOI := strings.ToLower(strings.TrimSpace(newest.DOI))
		if olderDOI != "" && newestDOI != "" && olderDOI != newestDOI {
			return fmt.Errorf("cannot replace documents with different DOIs")
		}

		var familyIDs []uuid.UUID
		if err := tx.Model(&types.Document{}).
			Where("id = ? OR canonical_document_id = ?", olderID, olderID).
			Pluck("id", &familyIDs).Error; err != nil {
			return err
		}
		var sourceKeys, markdownKeys, assetKeys []string
		if err := tx.Model(&types.Document{}).
			Where("id IN ?", familyIDs).Pluck("storage_key", &sourceKeys).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.DocumentMarkdown{}).
			Where("document_id IN ?", familyIDs).Pluck("storage_key", &markdownKeys).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.Block{}).
			Where("document_id IN ? AND asset_key <> ''", familyIDs).
			Pluck("asset_key", &assetKeys).Error; err != nil {
			return err
		}
		cleanup.StorageKeys = uniqueDocumentStorageKeys(sourceKeys, markdownKeys, assetKeys)

		// A citation owned by the newest document that points at any removed
		// family member would become a self-citation when it is repointed. It
		// describes the same paper citing itself, so it is dropped.
		if err := tx.Exec(`DELETE FROM paper_citations
			WHERE citing_document_id = ? AND cited_document_id IN ?`,
			newestID, familyIDs).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Exec(`UPDATE paper_citations
			SET cited_document_id = ?, resolution_status = 'succeeded',
			    resolution_generation = CASE
			        WHEN resolution_status IN ('queued', 'running')
			            THEN resolution_generation + 1
			        ELSE resolution_generation
			    END,
			    resolution_provider = '', resolution_error_code = NULL,
			    resolution_error_message = NULL, resolution_updated_at = ?, updated_at = ?
			WHERE cited_document_id IN ?`,
			newestID, now, now, familyIDs).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO document_base_files
				(document_base_id, document_id, added_at, sort_order, metadata)
			SELECT document_base_id, ?, added_at, sort_order, metadata
			FROM document_base_files
			WHERE document_id IN ?
			ON CONFLICT (document_base_id, document_id) DO NOTHING`,
			newestID, familyIDs).Error; err != nil {
			return err
		}
		familyIDTexts := make([]string, 0, len(familyIDs))
		for _, id := range familyIDs {
			familyIDTexts = append(familyIDTexts, id.String())
		}
		if err := tx.Where(
			"(scope = ? AND scope_id IN ?) OR related_id IN ?",
			types.TaskScopeDocument, familyIDTexts, familyIDTexts,
		).Delete(&types.TaskDeadLetter{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&types.Document{}, "id = ?", olderID).Error; err != nil {
			return err
		}
		if olderDOI != "" && newestDOI == "" {
			result := tx.Model(&types.Document{}).
				Where("id = ? AND doi = ''", newestID).
				Update("doi", olderDOI)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("newest document DOI changed during replacement")
			}
		}
		return nil
	})
	if err != nil {
		logger.Error(ctx, "replace older document failed",
			"older_document_id", olderID, "newest_document_id", newestID, "error", err)
		return types.DocumentFileKeys{}, err
	}
	logger.Info(ctx, "older document replaced by newest",
		"older_document_id", olderID, "newest_document_id", newestID)
	return cleanup, nil
}

func uniqueDocumentStorageKeys(groups ...[]string) []string {
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	for _, group := range groups {
		for _, key := range group {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func isDuplicateDOIError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == "uq_documents_doi_nonempty"
}

func isDuplicateFileHashError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == "uq_documents_file_sha256_nonempty"
}

func repositoryError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}

var _ interfaces.DocumentRepository = (*documentRepository)(nil)
