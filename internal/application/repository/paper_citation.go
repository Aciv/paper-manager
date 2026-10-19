package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

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

func (r *paperCitationRepository) AddCitation(
	ctx context.Context, citation types.PaperCitation,
) (types.PaperCitation, error) {
	if err := preparePaperCitation(&citation); err != nil {
		return types.PaperCitation{}, err
	}
	citation.Origin = types.PaperCitationOriginManual
	var result types.PaperCitation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing types.PaperCitation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("citing_document_id = ? AND reference_key = ?", citation.CitingDocumentID, citation.ReferenceKey).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if createErr := tx.Create(&citation).Error; createErr != nil {
				// A concurrent writer may have inserted the same owner/key.
				if findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("citing_document_id = ? AND reference_key = ?", citation.CitingDocumentID, citation.ReferenceKey).
					First(&existing).Error; findErr != nil {
					return createErr
				}
				mergeManualCitation(&existing, citation)
				if saveErr := tx.Save(&existing).Error; saveErr != nil {
					return saveErr
				}
				result = existing
				return nil
			}
			result = citation
			return nil
		}
		if err != nil {
			return err
		}
		mergeManualCitation(&existing, citation)
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		result = existing
		return nil
	})
	if err != nil {
		logger.Error(ctx, "create paper citation failed",
			"citing_document_id", citation.CitingDocumentID,
			"reference_id", citation.ReferenceID,
			"error", err)
		return types.PaperCitation{}, err
	}
	logger.Info(ctx, "paper citation saved",
		"citing_document_id", result.CitingDocumentID,
		"reference_id", result.ReferenceID)
	return result, nil
}

func mergeManualCitation(existing *types.PaperCitation, incoming types.PaperCitation) {
	existing.PaperReference = mergeReferenceMetadata(existing.PaperReference, incoming.PaperReference, nil)
	existing.RawReference = incoming.RawReference
	existing.Origin = types.PaperCitationOriginManual
	if incoming.CitedDocumentID != nil {
		existing.CitedDocumentID = incoming.CitedDocumentID
		existing.ResolutionStatus = types.DocumentStageSucceeded
		existing.ResolutionErrorCode = nil
		existing.ResolutionErrorMsg = nil
		existing.ResolutionUpdatedAt = time.Now().UTC()
	}
	existing.UpdatedAt = time.Now().UTC()
}

// MergeAutomaticCitations reconciles only extraction-origin rows. It takes a
// FOR UPDATE lock over one bibliography, preserving manual rows.
func (r *paperCitationRepository) MergeAutomaticCitations(
	ctx context.Context,
	citingDocumentID uuid.UUID,
	incoming []types.PaperCitation,
) (items []types.PaperCitation, outputErr error) {
	automatic, err := normalizedAutomaticPaperCitations(citingDocumentID, incoming)
	if err != nil {
		return nil, err
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing := make([]types.PaperCitation, 0)
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("citing_document_id = ?", citingDocumentID).
			Order("created_at ASC, id").
			Find(&existing).Error; err != nil {
			return err
		}
		existingByKey := make(map[string]types.PaperCitation, len(existing))
		manualKeys := make(map[string]struct{}, len(existing))
		for _, citation := range existing {
			existingByKey[citation.ReferenceKey] = citation
			if citation.EffectiveOrigin() == types.PaperCitationOriginManual {
				manualKeys[citation.ReferenceKey] = struct{}{}
			}
		}
		incomingKeys := make(map[string]struct{}, len(automatic))
		for _, citation := range automatic {
			incomingKeys[citation.ReferenceKey] = struct{}{}
		}

		// Delete only extraction rows that disappeared from the bibliography.
		// Manual rows are never removed by an automatic refresh.
		staleIDs := make([]uuid.UUID, 0)
		for _, citation := range existing {
			if citation.EffectiveOrigin() != types.PaperCitationOriginExtraction {
				continue
			}
			if _, retained := incomingKeys[citation.ReferenceKey]; retained {
				continue
			}
			staleIDs = append(staleIDs, citation.ReferenceID)
		}
		if len(staleIDs) > 0 {
			if err := tx.Where("id IN ?", staleIDs).Delete(&types.PaperCitation{}).Error; err != nil {
				return err
			}
		}

		toUpsert := make([]types.PaperCitation, 0, len(automatic))
		for _, citation := range automatic {
			if _, manual := manualKeys[citation.ReferenceKey]; manual {
				continue
			}
			if previous, found := existingByKey[citation.ReferenceKey]; found &&
				paperCitationsEquivalentForMerge(previous, citation) {
				continue
			}
			toUpsert = append(toUpsert, citation)
		}
		if len(toUpsert) > 0 {
			// Download state is never changed by extraction reconciliation.
			// Leaving it out of the conflict update preserves a concurrently
			// started reference download.
			updates := clause.AssignmentColumns([]string{
				"raw_reference", "doi", "title", "authors", "venue", "published_at",
				"volume", "issue", "pages", "source_url", "metadata_source",
				"resolution_status", "resolution_generation", "resolution_provider",
				"resolution_error_code", "resolution_error_message", "resolution_updated_at",
				"cited_document_id", "origin",
			})
			updates = append(updates, clause.Assignment{
				Column: clause.Column{Name: "updated_at"},
				Value:  gorm.Expr("CURRENT_TIMESTAMP"),
			})
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "citing_document_id"}, {Name: "reference_key"}},
				DoUpdates: updates,
				// A concurrent manual write must win over an extraction refresh.
				Where: clause.Where{Exprs: []clause.Expression{
					clause.Eq{
						Column: clause.Column{Table: "paper_citations", Name: "origin"},
						Value:  types.PaperCitationOriginExtraction,
					},
				}},
			}).CreateInBatches(&toUpsert, insertBatchSize).Error; err != nil {
				return err
			}
		}
		items = make([]types.PaperCitation, 0, len(existing)+len(toUpsert))
		if err := tx.Where("citing_document_id = ?", citingDocumentID).
			Order("created_at ASC, id").
			Find(&items).Error; err != nil {
			return err
		}
		for index := range items {
			normalizeReferenceSlice(&items[index].PaperReference)
		}
		return nil
	})
	if err != nil {
		logger.Error(ctx, "merge automatic paper citations failed",
			"citing_document_id", citingDocumentID,
			"citation_count", len(automatic), "error", err)
		return nil, err
	}
	logger.Info(ctx, "automatic paper citations reconciled",
		"citing_document_id", citingDocumentID,
		"citation_count", len(items))
	return items, nil
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
		return tx.Create(&citations).Error
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
	referenceID uuid.UUID,
) error {
	if citingDocumentID == uuid.Nil || referenceID == uuid.Nil {
		return types.ErrInvalidPaperCitation
	}
	result := r.db.WithContext(ctx).
		Where("citing_document_id = ? AND id = ?", citingDocumentID, referenceID).
		Delete(&types.PaperCitation{})
	if result.Error != nil {
		logger.Error(ctx, "delete paper citation failed",
			"citing_document_id", citingDocumentID,
			"reference_id", referenceID,
			"error", result.Error)
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sql.ErrNoRows
	}
	logger.Info(ctx, "paper citation deleted",
		"citing_document_id", citingDocumentID,
		"reference_id", referenceID)
	return nil
}

func (r *paperCitationRepository) ListByCitingDocumentID(
	ctx context.Context,
	citingDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	citations := make([]types.PaperCitation, 0)
	err := r.db.WithContext(ctx).
		Where("citing_document_id = ?", citingDocumentID).
		Order("created_at DESC, id").
		Find(&citations).Error
	if err != nil {
		logger.Error(ctx, "list cited references failed",
			"citing_document_id", citingDocumentID,
			"error", err)
		return nil, err
	}
	for index := range citations {
		normalizeReferenceSlice(&citations[index].PaperReference)
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
		Order("created_at DESC, id").Offset(offset).Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return types.PaperCitationPage{}, err
	}
	page := types.PaperCitationPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		page.HasMore = true
		page.NextOffset = offset + limit
	}
	for index := range page.Items {
		normalizeReferenceSlice(&page.Items[index].PaperReference)
	}
	return page, nil
}

func normalizedPaperCitations(
	citingDocumentID uuid.UUID,
	replacements []types.PaperCitation,
) ([]types.PaperCitation, error) {
	if citingDocumentID == uuid.Nil {
		return nil, types.ErrInvalidPaperCitation
	}
	seen := make(map[string]struct{}, len(replacements))
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
		if citation.Origin == "" {
			citation.Origin = types.PaperCitationOriginManual
		}
		if err := preparePaperCitation(&citation); err != nil {
			return nil, err
		}
		if _, exists := seen[citation.ReferenceKey]; exists {
			continue
		}
		seen[citation.ReferenceKey] = struct{}{}
		citations = append(citations, citation)
	}
	return citations, nil
}

// paperCitationsEquivalentForMerge reports whether an automatic refresh would
// write exactly the row already stored. Timestamps are excluded because they
// are persistence annotations, not bibliography content.
func paperCitationsEquivalentForMerge(existing, incoming types.PaperCitation) bool {
	existing.CreatedAt = time.Time{}
	existing.UpdatedAt = time.Time{}
	incoming.CreatedAt = time.Time{}
	incoming.UpdatedAt = time.Time{}
	return reflect.DeepEqual(existing, incoming)
}

func normalizedAutomaticPaperCitations(
	citingDocumentID uuid.UUID,
	citations []types.PaperCitation,
) ([]types.PaperCitation, error) {
	if citingDocumentID == uuid.Nil {
		return nil, types.ErrInvalidPaperCitation
	}
	seen := make(map[string]struct{}, len(citations))
	result := make([]types.PaperCitation, 0, len(citations))
	for _, citation := range citations {
		if citation.CitingDocumentID != uuid.Nil && citation.CitingDocumentID != citingDocumentID {
			return nil, fmt.Errorf("%w: citing document %s does not match automatic owner %s",
				types.ErrInvalidPaperCitation, citation.CitingDocumentID, citingDocumentID)
		}
		citation.CitingDocumentID = citingDocumentID
		citation.Origin = types.PaperCitationOriginExtraction
		if citation.CitedDocumentID != nil && *citation.CitedDocumentID == citingDocumentID {
			citation.CitedDocumentID = nil
			citation.ResolutionStatus = types.DocumentStageNotStarted
		}
		if err := preparePaperCitation(&citation); err != nil {
			return nil, err
		}
		if _, exists := seen[citation.ReferenceKey]; exists {
			continue
		}
		seen[citation.ReferenceKey] = struct{}{}
		result = append(result, citation)
	}
	return result, nil
}

func preparePaperCitation(citation *types.PaperCitation) error {
	if citation.CitingDocumentID == uuid.Nil {
		return types.ErrInvalidPaperCitation
	}
	if citation.ReferenceID == uuid.Nil {
		citation.ReferenceID = types.PaperReferenceIDForRaw(citation.CitingDocumentID, citation.RawReference)
	}
	citation.ReferenceKey = types.PaperCitationReferenceKey(citation.RawReference)
	if citation.ReferenceKey == "" {
		return types.ErrInvalidPaperCitation
	}
	if citation.Origin == "" {
		citation.Origin = types.PaperCitationOriginManual
	}
	if citation.ResolutionStatus == "" {
		citation.ResolutionStatus = types.DocumentStageNotStarted
	}
	if citation.CitedDocumentID != nil && !citation.ResolutionStatus.Terminal() {
		citation.ResolutionStatus = types.DocumentStageSucceeded
	}
	if citation.MetadataSource == "" {
		citation.MetadataSource = types.PaperMetadataSourceUnknown
	}
	if citation.Authors == nil {
		citation.Authors = []types.PaperAuthor{}
	}
	if citation.ResolutionUpdatedAt.IsZero() {
		citation.ResolutionUpdatedAt = time.Now().UTC()
	}
	return citation.Validate()
}

var _ interfaces.PaperCitationRepository = (*paperCitationRepository)(nil)
