package repository

import (
	"context"
	"slices"
	"strings"
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
		span.Scope = types.ProcessingSpanScopeDocument
	}
	if span.ScopeID == "" {
		span.ScopeID = types.ComposeProcessingSpanScopeID(span.Kind, span.DocumentID.String())
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

func (r *documentProcessingSpanRepository) Heartbeat(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).
		Where("id = ? AND status IN ?", id, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).
		Update("updated_at", time.Now().UTC()).Error
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
	var rows []types.DocumentProcessingSpan
	if err := r.db.WithContext(ctx).Where("document_id = ?", documentID).Order("attempt DESC, updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	latest := make(map[string]types.DocumentProcessingSpan, len(rows))
	for _, row := range rows {
		// scope_id is part of the unique tuple. It keeps separate reference
		// runs and provider calls distinct while collapsing queue retries.
		key := string(row.Kind) + "\x00" + string(row.Scope) + "\x00" + row.ScopeID.String()
		if _, exists := latest[key]; !exists {
			latest[key] = row
		}
	}
	result := make([]types.DocumentProcessingSpan, 0, len(latest))
	for _, row := range latest {
		result = append(result, row)
	}
	slices.SortFunc(result, func(left, right types.DocumentProcessingSpan) int {
		if left.StartedAt.Before(right.StartedAt) {
			return -1
		}
		if left.StartedAt.After(right.StartedAt) {
			return 1
		}
		return strings.Compare(left.ID.String(), right.ID.String())
	})
	return result, nil
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

func (r *documentProcessingSpanRepository) HasActiveScope(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND scope_id = ? AND status IN ?", documentID, kind, scope, scopeID, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Count(&count).Error
	return count > 0, err
}

func (r *documentProcessingSpanRepository) ListActiveScopeIDs(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope) ([]types.ProcessingSpanScopeID, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND status IN ?", documentID, kind, scope, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Distinct("scope_id").Pluck("scope_id", &ids).Error
	if err != nil {
		return nil, err
	}
	result := make([]types.ProcessingSpanScopeID, len(ids))
	for index, id := range ids {
		result[index] = types.ProcessingSpanScopeID(id)
	}
	return result, nil
}

func (r *documentProcessingSpanRepository) CancelActiveScope(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, code, reason string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND scope_id = ? AND status IN ?", documentID, kind, scope, scopeID, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Updates(map[string]interface{}{"status": types.ProcessingSpanCancelled, "error_code": code, "error_reason": reason, "finished_at": now}).Error
	if err != nil {
		logger.Error(ctx, "cancel scoped processing spans failed", "document_id", documentID, "scope", scope, "scope_id", scopeID, "error", err)
	}
	return err
}

func (r *documentProcessingSpanRepository) CancelActiveScopePrefix(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, prefix types.ProcessingSpanScopeID, code, reason string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND scope_id LIKE ? AND status IN ?", documentID, kind, scope, prefix+"%", []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).Updates(map[string]interface{}{"status": types.ProcessingSpanCancelled, "error_code": code, "error_reason": reason, "finished_at": now}).Error
	if err != nil {
		logger.Error(ctx, "cancel prefixed processing spans failed", "document_id", documentID, "scope", scope, "prefix", prefix, "error", err)
	}
	return err
}

func (r *documentProcessingSpanRepository) IsScopeCancelled(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND scope_id = ? AND status = ?", documentID, kind, scope, scopeID, types.ProcessingSpanCancelled).Count(&count).Error
	return count > 0, err
}

func (r *documentProcessingSpanRepository) ForceCancelScope(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID, code, reason string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND scope_id = ?", documentID, kind, scope, scopeID).Updates(map[string]interface{}{"status": types.ProcessingSpanCancelled, "error_code": code, "error_reason": reason, "finished_at": now}).Error
	if err != nil {
		logger.Error(ctx, "force cancel processing span failed", "document_id", documentID, "scope", scope, "scope_id", scopeID, "error", err)
	}
	return err
}

var _ interfaces.DocumentProcessingSpanRepository = (*documentProcessingSpanRepository)(nil)
