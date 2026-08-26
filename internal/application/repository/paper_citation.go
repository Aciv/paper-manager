package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type paperCitationRepository struct {
	db *gorm.DB
}

// NewPaperCitationRepository creates the GORM-backed citation repository.
func NewPaperCitationRepository(db *gorm.DB) interfaces.PaperCitationRepository {
	return &paperCitationRepository{db: db}
}

func (r *paperCitationRepository) AddCitation(ctx context.Context, citation types.PaperCitation) error {
	if err := citation.Validate(); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "citing_document_id"},
			{Name: "cited_document_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"raw_reference"}),
	}).Create(&citation)
	if result.Error != nil {
		logger.Error(ctx, "create paper citation failed",
			"citing_document_id", citation.CitingDocumentID,
			"cited_document_id", citation.CitedDocumentID,
			"error", result.Error)
		return result.Error
	}
	logger.Info(ctx, "paper citation saved",
		"citing_document_id", citation.CitingDocumentID,
		"cited_document_id", citation.CitedDocumentID)
	return nil
}

func (r *paperCitationRepository) RepointCitation(
	ctx context.Context,
	citingDocumentID uuid.UUID,
	oldCitedDocumentID uuid.UUID,
	newCitedDocumentID uuid.UUID,
	rawReference string,
) error {
	replacement := types.PaperCitation{
		CitingDocumentID: citingDocumentID,
		CitedDocumentID:  newCitedDocumentID,
		RawReference:     rawReference,
	}
	if err := replacement.Validate(); err != nil {
		return err
	}
	old := types.PaperCitation{
		CitingDocumentID: citingDocumentID,
		CitedDocumentID:  oldCitedDocumentID,
	}
	if err := old.ValidateEndpoints(); err != nil {
		return err
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		deleted := tx.Where(
			"citing_document_id = ? AND cited_document_id = ?",
			citingDocumentID,
			oldCitedDocumentID,
		).Delete(&types.PaperCitation{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected == 0 {
			return sql.ErrNoRows
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "citing_document_id"},
				{Name: "cited_document_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"raw_reference"}),
		}).Create(&replacement).Error
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error(ctx, "repoint paper citation failed",
				"citing_document_id", citingDocumentID,
				"old_cited_document_id", oldCitedDocumentID,
				"new_cited_document_id", newCitedDocumentID,
				"error", err)
		}
		return err
	}
	logger.Info(ctx, "paper citation repointed",
		"citing_document_id", citingDocumentID,
		"old_cited_document_id", oldCitedDocumentID,
		"new_cited_document_id", newCitedDocumentID)
	return nil
}

func (r *paperCitationRepository) ReplaceCitations(
	ctx context.Context,
	citingDocumentID uuid.UUID,
	replacements []types.PaperCitation,
) error {
	citations, err := normalizedPaperCitations(citingDocumentID, replacements)
	if err != nil {
		return err
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("citing_document_id = ?", citingDocumentID).
			Delete(&types.PaperCitation{}).Error; err != nil {
			return err
		}
		if len(citations) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&citations).Error
	})
	if err != nil {
		logger.Error(ctx, "replace paper citations failed",
			"citing_document_id", citingDocumentID,
			"citation_count", len(citations),
			"error", err)
		return err
	}
	logger.Info(ctx, "paper citations replaced",
		"citing_document_id", citingDocumentID,
		"citation_count", len(citations))
	return nil
}

func (r *paperCitationRepository) RemoveCitation(
	ctx context.Context,
	citingDocumentID uuid.UUID,
	citedDocumentID uuid.UUID,
) error {
	citation := types.PaperCitation{
		CitingDocumentID: citingDocumentID,
		CitedDocumentID:  citedDocumentID,
	}
	if err := citation.ValidateEndpoints(); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).
		Where("citing_document_id = ? AND cited_document_id = ?", citingDocumentID, citedDocumentID).
		Delete(&types.PaperCitation{})
	if result.Error != nil {
		logger.Error(ctx, "delete paper citation failed",
			"citing_document_id", citingDocumentID,
			"cited_document_id", citedDocumentID,
			"error", result.Error)
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sql.ErrNoRows
	}
	logger.Info(ctx, "paper citation deleted",
		"citing_document_id", citingDocumentID,
		"cited_document_id", citedDocumentID)
	return nil
}

func (r *paperCitationRepository) ListByCitingDocumentID(
	ctx context.Context,
	citingDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	citations := make([]types.PaperCitation, 0)
	err := r.db.WithContext(ctx).
		Where("citing_document_id = ?", citingDocumentID).
		Order("created_at DESC, cited_document_id").
		Find(&citations).Error
	if err != nil {
		logger.Error(ctx, "list cited papers failed",
			"citing_document_id", citingDocumentID,
			"error", err)
		return nil, err
	}
	return citations, nil
}

func (r *paperCitationRepository) ListByCitingDocumentIDPage(
	ctx context.Context, citingDocumentID uuid.UUID, offset, limit int,
) (types.PaperCitationPage, error) {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows := make([]types.PaperCitation, 0, limit+1)
	err := r.db.WithContext(ctx).Where("citing_document_id = ?", citingDocumentID).
		Order("created_at DESC, cited_document_id").Offset(offset).Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return types.PaperCitationPage{}, err
	}
	page := types.PaperCitationPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		page.HasMore = true
		page.NextOffset = offset + limit
	}
	return page, nil
}

func (r *paperCitationRepository) ListByCitedDocumentID(
	ctx context.Context,
	citedDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	citations := make([]types.PaperCitation, 0)
	err := r.db.WithContext(ctx).
		Where("cited_document_id = ?", citedDocumentID).
		Order("created_at DESC, citing_document_id").
		Find(&citations).Error
	if err != nil {
		logger.Error(ctx, "list citing papers failed",
			"cited_document_id", citedDocumentID,
			"error", err)
		return nil, err
	}
	return citations, nil
}

func normalizedPaperCitations(
	citingDocumentID uuid.UUID,
	replacements []types.PaperCitation,
) ([]types.PaperCitation, error) {
	if citingDocumentID == uuid.Nil {
		return nil, types.ErrInvalidPaperCitation
	}
	seen := make(map[uuid.UUID]struct{}, len(replacements))
	citations := make([]types.PaperCitation, 0, len(replacements))
	for _, citation := range replacements {
		if citation.CitingDocumentID != uuid.Nil && citation.CitingDocumentID != citingDocumentID {
			return nil, fmt.Errorf(
				"%w: citing document %s does not match replacement owner %s",
				types.ErrInvalidPaperCitation,
				citation.CitingDocumentID,
				citingDocumentID,
			)
		}
		citation.CitingDocumentID = citingDocumentID
		if err := citation.Validate(); err != nil {
			return nil, err
		}
		if _, exists := seen[citation.CitedDocumentID]; exists {
			continue
		}
		seen[citation.CitedDocumentID] = struct{}{}
		citations = append(citations, citation)
	}
	return citations, nil
}

var _ interfaces.PaperCitationRepository = (*paperCitationRepository)(nil)
