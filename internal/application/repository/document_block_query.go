package repository

import (
	"context"
	"database/sql"
	"errors"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// QueryBlocks keeps the committed outline and blocks in one MVCC snapshot.
// The transaction ends before returning; no lock is held across agent calls.
func (r *documentRepository) QueryBlocks(
	ctx context.Context, documentID uuid.UUID, input types.DocumentBlockQuery,
) (types.DocumentBlockPage, error) {
	result := types.DocumentBlockPage{
		DocumentID: documentID.String(), Mode: input.Mode, Page: input.Page,
		PageSize: input.PageSize, Status: "succeeded",
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document types.Document
		if err := tx.Select("id", "source_state").First(&document, "id = ?", documentID).Error; err != nil {
			return repositoryError(err)
		}
		if document.SourceState != types.DocumentSourceAvailable {
			return sql.ErrNoRows
		}
		var manifest types.DocumentMarkdown
		if err := tx.First(&manifest, "document_id = ?", documentID).Error; err != nil {
			return repositoryError(err)
		}
		result.RecognitionGeneration = manifest.RecognitionGeneration
		if input.ExpectedGeneration != nil && *input.ExpectedGeneration != manifest.RecognitionGeneration {
			return types.ErrSourceSnapshotChanged
		}
		result.Range = input.Range
		if input.SectionOrdinal != nil {
			outline, err := types.ParseDocumentOutline(manifest.DocumentOutline)
			if err != nil {
				return err
			}
			section, blockRange, err := sourceSectionBlockRange(outline, *input.SectionOrdinal)
			if err != nil {
				return err
			}
			result.Section, result.Range = section, blockRange
		}
		query := tx.Model(&types.Block{}).Where("document_id = ?", documentID)
		if result.Range != nil {
			query = query.Where("order_index >= ? AND order_index < ?", result.Range.Start, result.Range.End)
		}
		if input.PageNumber > 0 {
			query = query.Where("page_number = ?", input.PageNumber)
		}
		if len(input.BlockIDs) > 0 {
			query = query.Where("id IN ?", input.BlockIDs)
			var found []string
			if err := query.Session(&gorm.Session{}).Pluck("id", &found).Error; err != nil {
				return err
			}
			present := make(map[string]bool, len(found))
			for _, id := range found {
				present[id] = true
			}
			for _, id := range input.BlockIDs {
				if !present[id] {
					result.MissingIDs = append(result.MissingIDs, id)
				}
			}
			result.Total = int64(len(found))
			if len(result.MissingIDs) > 0 {
				result.Status = "partial"
				if len(found) == 0 {
					result.Status = "failed"
				}
			}
		} else if err := query.Session(&gorm.Session{}).Count(&result.Total).Error; err != nil {
			return err
		}
		// Range-only replies do not select IDs or text unless explicitly requested.
		if input.Mode == "range" && !input.IncludeIDs {
			return nil
		}
		offset := (input.Page - 1) * input.PageSize
		result.HasMore = int64(offset+input.PageSize) < result.Total
		paged := query.Order("order_index ASC").Offset(offset).Limit(input.PageSize)
		if input.Mode == "range" {
			return paged.Pluck("id", &result.BlockIDs).Error
		}
		result.Items = []types.Block{}
		return paged.Find(&result.Items).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil && !errors.Is(err, sql.ErrNoRows) &&
		!errors.Is(err, types.ErrSourceSnapshotChanged) && !errors.Is(err, types.ErrSourceRangeUnavailable) &&
		!errors.Is(err, types.ErrInvalidBlockQuery) {
		logger.Error(ctx, "query document blocks failed", "document_id", documentID, "error", err)
	}
	return result, err
}

func sourceSectionBlockRange(
	outline types.DocumentOutline, ordinal int,
) (*types.DocumentOutlineSection, *types.BlockRange, error) {
	for index := range outline.Sections {
		section := outline.Sections[index]
		if section.Ordinal != ordinal {
			continue
		}
		if section.StartBlockOrdinal < 0 || section.EndBlockOrdinal < section.StartBlockOrdinal {
			return nil, nil, types.ErrSourceRangeUnavailable
		}
		end := section.EndBlockOrdinal
		for _, next := range outline.Sections[index+1:] {
			if next.Level > section.Level {
				continue
			}
			if next.StartBlockOrdinal < section.StartBlockOrdinal {
				return nil, nil, types.ErrSourceRangeUnavailable
			}
			end = next.StartBlockOrdinal
			break
		}
		return &section, &types.BlockRange{Start: section.StartBlockOrdinal, End: end}, nil
	}
	return nil, nil, types.ErrInvalidBlockQuery
}

func (r *documentRepository) MarkdownPageCounts(
	ctx context.Context, ids []uuid.UUID,
) (map[uuid.UUID]int, error) {
	result := make(map[uuid.UUID]int, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		DocumentID uuid.UUID
		PageCount  int
	}
	err := r.db.WithContext(ctx).Model(&types.DocumentMarkdown{}).
		Select("document_id", "page_count").Where("document_id IN ?", ids).Scan(&rows).Error
	if err != nil {
		logger.Error(ctx, "read document page counts failed", "document_count", len(ids), "error", err)
		return nil, err
	}
	for _, row := range rows {
		result[row.DocumentID] = row.PageCount
	}
	return result, nil
}
