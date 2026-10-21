package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type noteReadingRepository struct{ db *gorm.DB }

func NewNoteReadingRepository(db *gorm.DB) interfaces.NoteReadingRepository {
	return &noteReadingRepository{db: db}
}

func (r *noteReadingRepository) Get(ctx context.Context, documentID uuid.UUID) (types.DocumentReadingNote, error) {
	var note types.DocumentReadingNote
	err := repositoryError(r.db.WithContext(ctx).First(&note, "document_id = ?", documentID).Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document reading note failed", "document_id", documentID, "error", err)
	}
	if err == nil {
		err = note.ParseDirectory()
	}
	return note, err
}

func (r *noteReadingRepository) Put(ctx context.Context, candidate types.DocumentReadingNote, expectedRevision int64) (types.DocumentReadingNote, error) {
	documentID := candidate.DocumentID
	var persisted types.DocumentReadingNote
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var document types.Document
		if err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).First(&document, "id = ? AND source_state = ?", documentID, types.DocumentSourceAvailable).Error; err != nil {
			return repositoryError(err)
		}
		now := time.Now().UTC()
		if expectedRevision == 0 {
			persisted = candidate
			persisted.Revision, persisted.CreatedAt, persisted.UpdatedAt = 1, now, now
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&persisted)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return types.ErrReadingNoteConflict
			}
			return nil
		}
		var current types.DocumentReadingNote
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current,
			"document_id = ? AND revision = ?", documentID, expectedRevision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return types.ErrReadingNoteConflict
			}
			return err
		}
		result := tx.Model(&types.DocumentReadingNote{}).Where("document_id = ? AND revision = ?", documentID, expectedRevision).
			Updates(map[string]any{"structure": candidate.Structure, "size_bytes": candidate.SizeBytes,
				"revision": gorm.Expr("revision + 1"), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrReadingNoteConflict
		}
		return tx.First(&persisted, "document_id = ?", documentID).Error
	})
	if err != nil {
		if !errors.Is(err, types.ErrReadingNoteConflict) && !errors.Is(err, sql.ErrNoRows) {
			logger.Error(ctx, "put document reading note failed", "document_id", documentID, "error", err)
		}
		return types.DocumentReadingNote{}, err
	}
	if err := persisted.ParseDirectory(); err != nil {
		return types.DocumentReadingNote{}, err
	}
	logger.Info(ctx, "document reading note manifest persisted", "document_id", documentID, "revision", persisted.Revision)
	return persisted, nil
}

func (r *noteReadingRepository) Delete(ctx context.Context, documentID uuid.UUID, expectedRevision int64) (types.DocumentReadingNote, error) {
	var deleted types.DocumentReadingNote
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&deleted,
			"document_id = ? AND revision = ?", documentID, expectedRevision).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return types.ErrReadingNoteConflict
			}
			return err
		}
		return tx.Delete(&types.DocumentReadingNote{}, "document_id = ? AND revision = ?", documentID, expectedRevision).Error
	})
	if err != nil {
		if !errors.Is(err, types.ErrReadingNoteConflict) {
			logger.Error(ctx, "delete document reading note failed", "document_id", documentID, "error", err)
		}
		return types.DocumentReadingNote{}, err
	}
	logger.Info(ctx, "document reading note manifest deleted", "document_id", documentID, "revision", expectedRevision)
	if parseErr := deleted.ParseDirectory(); parseErr != nil {
		return types.DocumentReadingNote{}, parseErr
	}
	return deleted, nil
}
