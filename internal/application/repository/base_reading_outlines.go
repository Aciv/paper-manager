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

// ListReadingOutlines uses a fixed three SELECTs regardless of page size:
// base existence, membership count, and one projection joined to both manifests.
// It does not load Markdown bodies, blocks or per-document descriptions.
func (r *documentBaseRepository) ListReadingOutlines(
	ctx context.Context, baseID uuid.UUID, page, pageSize int,
) ([]types.BaseReadingOutlineRow, int64, error) {
	rows := []types.BaseReadingOutlineRow{}
	var total int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := (&documentBaseRepository{db: tx}).GetBase(ctx, baseID); err != nil {
			return err
		}
		members := tx.Table("document_base_files AS membership").
			Joins("JOIN documents AS doc ON doc.id = membership.document_id").
			Where("membership.document_base_id = ?", baseID)
		if err := members.Count(&total).Error; err != nil {
			return err
		}
		return members.Select(`doc.id AS document_id,
			COALESCE(NULLIF(paper.title, ''), doc.original_filename) AS title,
			doc.source_state, doc.markdown_status, doc.markdown_generation,
			md.recognition_generation AS source_generation,
			COALESCE(md.document_outline::text, '') AS source_outline_json,
			note.revision AS note_revision, COALESCE(note.size_bytes, 0) AS note_size_bytes,
			COALESCE(note.structure::text, '') AS note_structure_json`).
			Joins("LEFT JOIN paper_metadata AS paper ON paper.document_id = doc.id").
			Joins("LEFT JOIN document_markdowns AS md ON md.document_id = doc.id").
			Joins("LEFT JOIN document_reading_notes AS note ON note.document_id = doc.id").
			Order("membership.sort_order, membership.added_at DESC, membership.document_id").
			Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error(ctx, "list base reading outlines failed", "document_base_id", baseID, "error", err)
		}
		return nil, 0, err
	}
	return rows, total, nil
}
