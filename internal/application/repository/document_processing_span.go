package repository

import (
	"context"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type documentProcessingSpanRepository struct{ db *gorm.DB }

func NewDocumentProcessingSpanRepository(db *gorm.DB) interfaces.DocumentProcessingSpanRepository {
	return &documentProcessingSpanRepository{db: db}
}

func (r *documentProcessingSpanRepository) UpsertRunning(ctx context.Context, span types.DocumentProcessingSpan) (types.DocumentProcessingSpan, error) {
	if span.ID == uuid.Nil {
		span.ID = uuid.New()
	}
	if span.Attempt < 1 {
		span.Attempt = 1
	}
	if span.Scope == "" {
		span.Scope = types.TaskScopeDocument
	}
	if span.ScopeID == "" {
		span.ScopeID = span.DocumentID.String()
	}
	if len(span.Attributes) == 0 {
		span.Attributes = types.JSON(`{}`)
	}
	now := time.Now().UTC()
	span.Status, span.StartedAt, span.FinishedAt = types.ProcessingSpanRunning, now, nil
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "document_id"}, {Name: "attempt"}, {Name: "kind"}, {Name: "scope"}, {Name: "scope_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status": types.ProcessingSpanRunning, "retry_count": span.RetryCount,
			"error_code": "", "error_reason": "", "finished_at": nil, "updated_at": now,
		}),
	}).Create(&span).Error
	if err != nil {
		logger.Error(ctx, "upsert processing span failed", "document_id", span.DocumentID, "kind", span.Kind, "error", err)
		return types.DocumentProcessingSpan{}, err
	}
	var stored types.DocumentProcessingSpan
	err = r.db.WithContext(ctx).Where("document_id = ? AND attempt = ? AND kind = ? AND scope = ? AND scope_id = ?", span.DocumentID, span.Attempt, span.Kind, span.Scope, span.ScopeID).First(&stored).Error
	return stored, err
}

func (r *documentProcessingSpanRepository) Finish(ctx context.Context, id uuid.UUID, status types.ProcessingSpanStatus, code, reason string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("id = ? AND status IN ?", id, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Updates(map[string]interface{}{"status": status, "error_code": code, "error_reason": reason, "finished_at": now}).Error
	if err != nil {
		logger.Error(ctx, "finish processing span failed", "span_id", id, "error", err)
	}
	return err
}

func (r *documentProcessingSpanRepository) FinishAttempt(ctx context.Context, documentID uuid.UUID, attempt int, status types.ProcessingSpanStatus, code, reason string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND attempt = ? AND status IN ?", documentID, attempt, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Updates(map[string]interface{}{"status": status, "error_code": code, "error_reason": reason, "finished_at": now}).Error
	return err
}

func (r *documentProcessingSpanRepository) ListLatestAttempt(ctx context.Context, documentID uuid.UUID) ([]types.DocumentProcessingSpan, error) {
	var attempt int
	if err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ?", documentID).Select("COALESCE(MAX(attempt), 0)").Scan(&attempt).Error; err != nil {
		return nil, err
	}
	if attempt == 0 {
		return []types.DocumentProcessingSpan{}, nil
	}
	var rows []types.DocumentProcessingSpan
	err := r.db.WithContext(ctx).Where("document_id = ? AND attempt = ?", documentID, attempt).Order("created_at ASC").Find(&rows).Error
	return rows, err
}

func (r *documentProcessingSpanRepository) HasActive(ctx context.Context, documentID uuid.UUID, kinds ...types.ProcessingSpanKind) (bool, error) {
	q := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND status IN ?", documentID, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning})
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}
	var count int64
	err := q.Count(&count).Error
	return count > 0, err
}

func (r *documentProcessingSpanRepository) CancelActive(ctx context.Context, documentID uuid.UUID, code, reason string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND status IN ?", documentID, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Updates(map[string]interface{}{"status": types.ProcessingSpanCancelled, "error_code": code, "error_reason": reason, "finished_at": now}).Error
	if err != nil {
		logger.Error(ctx, "cancel processing spans failed", "document_id", documentID, "error", err)
	}
	return err
}

var _ interfaces.DocumentProcessingSpanRepository = (*documentProcessingSpanRepository)(nil)
