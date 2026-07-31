// Package repository contains database operations for application tables.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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

func (r *documentRepository) EnsureUnknown(
	ctx context.Context, id uuid.UUID,
) (types.Document, bool, error) {
	if id == uuid.Nil {
		return types.Document{}, false, errors.New("unknown document id is required")
	}
	placeholder := types.Document{
		ID:               id,
		OriginalFilename: "",
		ContentType:      types.UnknownDocumentContentType,
		SizeBytes:        0,
		DOI:              "",
		StorageKey:       types.UnknownDocumentStorageScheme + id.String(),
		Status:           types.DocumentStatusUnknown,
	}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&placeholder)
	if result.Error != nil {
		logger.Error(ctx, "ensure unknown document failed", "document_id", id, "error", result.Error)
		return types.Document{}, false, result.Error
	}
	document, err := r.Get(ctx, id)
	if err != nil {
		return types.Document{}, false, err
	}
	if document.Status != types.DocumentStatusUnknown {
		return types.Document{}, false, fmt.Errorf("%w: %s", types.ErrUnknownDocumentConflict, id)
	}
	created := result.RowsAffected > 0
	if created {
		logger.Info(ctx, "unknown document created", "document_id", id)
	}
	return document, created, nil
}

func (r *documentRepository) DeleteUnknownIfUnreferenced(
	ctx context.Context, id uuid.UUID,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Where(
			`id = ? AND status = ? AND NOT EXISTS (
				SELECT 1 FROM paper_citations
				WHERE citing_document_id = documents.id OR cited_document_id = documents.id
			)`,
			id,
			types.DocumentStatusUnknown,
		).
		Delete(&types.Document{})
	if result.Error != nil {
		logger.Error(ctx, "delete unreferenced unknown document failed", "document_id", id, "error", result.Error)
		return false, result.Error
	}
	deleted := result.RowsAffected > 0
	if deleted {
		logger.Info(ctx, "unreferenced unknown document deleted", "document_id", id)
	}
	return deleted, nil
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

	// <> 是不等于
	//query := r.db.WithContext(ctx).Model(&types.Document{}).
	//	Where("documents.status <> ?", types.DocumentStatusUnknown)
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
	if search.Status != "" {
		query = query.Where("documents.status = ?", search.Status)
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

func (r *documentRepository) GetMarkdown(ctx context.Context, documentID uuid.UUID) (types.DocumentMarkdown, error) {
	var markdown types.DocumentMarkdown
	err := repositoryError(r.db.WithContext(ctx).First(&markdown, "document_id = ?", documentID).Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document markdown failed", "document_id", documentID, "error", err)
	}
	return markdown, err
}

func (r *documentRepository) ReplaceParsedOutput(
	ctx context.Context, markdown types.DocumentMarkdown, blocks []types.Block,
) error {
	if markdown.DocumentID == uuid.Nil {
		return errors.New("markdown document id is required")
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&types.Document{}).
			Where("id = ? AND status = ?", markdown.DocumentID, types.DocumentStatusProcessing).
			Updates(map[string]interface{}{"status": types.DocumentStatusReady, "error_message": nil})
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
			DoUpdates: clause.AssignmentColumns([]string{"storage_key", "filename", "content_type", "size_bytes", "sha256", "parser_name", "parser_version", "input_mode", "language", "page_count", "metadata", "updated_at"}),
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

func (r *documentRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status types.DocumentStatus, message *string) error {
	result := r.db.WithContext(ctx).Model(&types.Document{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": status, "error_message": message})
	if result.Error != nil {
		logger.Error(ctx, "update document status failed", "document_id", id, "status", status, "error", result.Error)
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sql.ErrNoRows
	}
	logger.Info(ctx, "document status updated", "document_id", id, "status", status)
	return nil
}

func (r *documentRepository) UpdateStatusIfCurrent(ctx context.Context, id uuid.UUID, status types.DocumentStatus, message *string, expected []types.DocumentStatus) (bool, error) {
	if len(expected) == 0 {
		return false, nil
	}
	result := r.db.WithContext(ctx).Model(&types.Document{}).Where("id = ? AND status IN ?", id, expected).
		Updates(map[string]interface{}{"status": status, "error_message": message})
	if result.Error != nil {
		logger.Error(ctx, "conditionally update document status failed", "document_id", id, "status", status, "error", result.Error)
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		logger.Info(ctx, "document status conditionally updated", "document_id", id, "status", status)
		return true, nil
	}
	return false, nil
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
