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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDuplicateDocumentBaseName = errors.New("document base with this name already exists")

type documentBaseRepository struct {
	db *gorm.DB
}

// NewDocumentBaseRepository creates the GORM-backed collection repository.
func NewDocumentBaseRepository(db *gorm.DB) interfaces.DocumentBaseRepository {
	return &documentBaseRepository{db: db}
}

func (r *documentBaseRepository) CreateBase(ctx context.Context, base types.DocumentBase) (types.DocumentBase, error) {
	base.Name = strings.TrimSpace(base.Name)
	if base.Settings == nil {
		base.Settings = types.JSON(`{}`)
	}
	err := r.db.WithContext(ctx).Create(&base).Error
	if isDuplicateBaseNameError(err) {
		err = fmt.Errorf("%w: %s", ErrDuplicateDocumentBaseName, base.Name)
	}
	if err != nil {
		logger.Error(ctx, "create document base failed", "document_base_id", base.ID, "error", err)
		return types.DocumentBase{}, err
	}
	logger.Info(ctx, "document base created", "document_base_id", base.ID)
	return base, nil
}

func (r *documentBaseRepository) GetBase(ctx context.Context, id uuid.UUID) (types.DocumentBase, error) {
	var base types.DocumentBase
	err := repositoryError(r.db.WithContext(ctx).First(&base, "id = ?", id).Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document base failed", "document_base_id", id, "error", err)
	}
	return base, err
}

func (r *documentBaseRepository) ListBases(ctx context.Context) ([]types.DocumentBase, error) {
	var bases []types.DocumentBase
	err := r.db.WithContext(ctx).Order("created_at DESC").Find(&bases).Error
	if err != nil {
		logger.Error(ctx, "list document bases failed", "error", err)
		return nil, err
	}
	return bases, nil
}

func (r *documentBaseRepository) UpdateBase(ctx context.Context, id uuid.UUID, name, description string) (types.DocumentBase, error) {
	result := r.db.WithContext(ctx).Model(&types.DocumentBase{}).Where("id = ?", id).
		Updates(map[string]interface{}{"name": strings.TrimSpace(name), "description": description})
	if isDuplicateBaseNameError(result.Error) {
		result.Error = fmt.Errorf("%w: %s", ErrDuplicateDocumentBaseName, name)
	}
	if result.Error != nil {
		logger.Error(ctx, "update document base failed", "document_base_id", id, "error", result.Error)
		return types.DocumentBase{}, result.Error
	}
	if result.RowsAffected == 0 {
		return types.DocumentBase{}, sql.ErrNoRows
	}
	updated, err := r.GetBase(ctx, id)
	if err == nil {
		logger.Info(ctx, "document base updated", "document_base_id", id)
	}
	return updated, err
}

func (r *documentBaseRepository) DeleteBase(ctx context.Context, id uuid.UUID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		memberships := tx.Where("document_base_id = ?", id).Delete(&types.DocumentBaseFile{})
		if memberships.Error != nil {
			return memberships.Error
		}
		result := tx.Delete(&types.DocumentBase{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return sql.ErrNoRows
		}
		logger.Info(ctx, "document base memberships deleted", "document_base_id", id, "memberships_deleted", memberships.RowsAffected)
		return nil
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error(ctx, "delete document base failed", "document_base_id", id, "error", err)
		}
		return err
	}
	logger.Info(ctx, "document base deleted", "document_base_id", id)
	return nil
}

func (r *documentBaseRepository) AddDocument(ctx context.Context, membership types.DocumentBaseFile) error {
	if membership.Metadata == nil {
		membership.Metadata = types.JSON(`{}`)
	}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&membership)
	if result.Error != nil {
		logger.Error(ctx, "add document to base failed", "document_base_id", membership.DocumentBaseID, "document_id", membership.DocumentID, "error", result.Error)
		return result.Error
	}
	if result.RowsAffected > 0 {
		logger.Info(ctx, "document added to base", "document_base_id", membership.DocumentBaseID, "document_id", membership.DocumentID)
	}
	return nil
}

func (r *documentBaseRepository) RemoveDocument(ctx context.Context, baseID, documentID uuid.UUID) error {
	result := r.db.WithContext(ctx).Where("document_base_id = ? AND document_id = ?", baseID, documentID).
		Delete(&types.DocumentBaseFile{})
	if result.Error != nil {
		logger.Error(ctx, "remove document from base failed", "document_base_id", baseID, "document_id", documentID, "error", result.Error)
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sql.ErrNoRows
	}
	logger.Info(ctx, "document removed from base", "document_base_id", baseID, "document_id", documentID)
	return nil
}

func (r *documentBaseRepository) ListDocuments(
	ctx context.Context, baseID uuid.UUID, page, pageSize int, excludeUnknown bool,
) (types.DocumentPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = types.DocumentPageSize
	}
	result := types.DocumentPage{Page: page, PageSize: pageSize, Items: []types.Document{}}
	query := r.db.WithContext(ctx).Model(&types.Document{}).
		Joins("JOIN document_base_files ON document_base_files.document_id = documents.id").
		Where("document_base_files.document_base_id = ?", baseID)
	if excludeUnknown {
		query = query.Where("documents.status <> ?", types.DocumentStatusUnknown)
	}
	if err := query.Count(&result.Total).Error; err != nil {
		logger.Error(ctx, "count document base members failed", "document_base_id", baseID, "error", err)
		return result, err
	}
	err := query.Order("document_base_files.sort_order, document_base_files.added_at DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&result.Items).Error
	if err != nil {
		logger.Error(ctx, "list document base members failed", "document_base_id", baseID, "error", err)
	}
	return result, err
}

func isDuplicateBaseNameError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == "uq_document_bases_name_lower"
}

var _ interfaces.DocumentBaseRepository = (*documentBaseRepository)(nil)
