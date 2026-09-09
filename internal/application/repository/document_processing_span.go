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
			"run_id":         gorm.Expr("COALESCE(document_processing_spans.run_id, excluded.run_id)"),
			"parent_span_id": gorm.Expr("COALESCE(document_processing_spans.parent_span_id, excluded.parent_span_id)"),
			"error_code":     "", "error_reason": "", "finished_at": nil, "updated_at": now,
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL:  "document_processing_spans.status IN ?",
			Vars: []interface{}{[]types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}},
		}}},
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

func (r *documentProcessingSpanRepository) ListCurrent(
	ctx context.Context, documentID uuid.UUID, runIDs []uuid.UUID, limit int,
) ([]types.DocumentProcessingSpan, error) {
	limit = boundedSpanQueryLimit(limit)
	where, args := "document_id = ?", []interface{}{documentID}
	if len(runIDs) > 0 {
		where, args = "(document_id = ? OR run_id IN ?)", []interface{}{documentID, runIDs}
	}
	query := `WITH ranked AS (
		SELECT document_processing_spans.*,
			ROW_NUMBER() OVER (
				PARTITION BY document_id, kind, scope, scope_id
				ORDER BY attempt DESC, updated_at DESC, id DESC
			) AS stage_rank
		FROM document_processing_spans
		WHERE ` + where + `
	), limited AS (
		SELECT * FROM ranked WHERE stage_rank = 1
		ORDER BY CASE WHEN scope IN ('process','document','source','reference_run','reference_download_run') THEN 0 ELSE 1 END,
			updated_at DESC, id DESC LIMIT ?
	)
	SELECT * FROM limited ORDER BY started_at ASC, id ASC`
	args = append(args, limit)
	rows := []types.DocumentProcessingSpan{}
	if err := r.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *documentProcessingSpanRepository) ListTimeline(
	ctx context.Context, documentID uuid.UUID, limit int,
) ([]types.DocumentProcessingSpan, error) {
	rows := []types.DocumentProcessingSpan{}
	err := r.db.WithContext(ctx).Where("document_id = ?", documentID).
		Order("updated_at DESC, id DESC").Limit(boundedSpanQueryLimit(limit)).Find(&rows).Error
	return rows, err
}

func (r *documentProcessingSpanRepository) LatestAttempt(
	ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind,
) (int, error) {
	var latest int64
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).
		Where("document_id = ? AND kind = ?", documentID, kind).
		Select("COALESCE(MAX(attempt), 0)").Scan(&latest).Error
	if err != nil {
		return 0, err
	}
	return int(latest), nil
}

func boundedSpanQueryLimit(limit int) int {
	if limit < 1 {
		return 100
	}
	if limit > 100 {
		return 100
	}
	return limit
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

func (r *documentProcessingSpanRepository) CancelActiveRun(ctx context.Context, runID uuid.UUID, code, reason string) error {
	if runID == uuid.Nil {
		return nil
	}
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).
		Where("run_id = ? AND status IN ?", runID, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).
		Updates(map[string]interface{}{
			"status": types.ProcessingSpanCancelled, "error_code": code,
			"error_reason": reason, "finished_at": now, "updated_at": now,
		}).Error
	if err != nil {
		logger.Error(ctx, "cancel processing run spans failed", "run_id", runID, "error", err)
	}
	return err
}

func (r *documentProcessingSpanRepository) IsScopeCancelled(ctx context.Context, documentID uuid.UUID, kind types.ProcessingSpanKind, scope types.ProcessingSpanScope, scopeID types.ProcessingSpanScopeID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.DocumentProcessingSpan{}).Where("document_id = ? AND kind = ? AND scope = ? AND scope_id = ? AND status = ?", documentID, kind, scope, scopeID, types.ProcessingSpanCancelled).Count(&count).Error
	return count > 0, err
}

func (r *documentProcessingSpanRepository) AggregateParentDisplay(
	ctx context.Context, childSpanID uuid.UUID,
) (types.ProcessingSpanStatus, error) {
	if childSpanID == uuid.Nil {
		return "", nil
	}
	var parentStatus types.ProcessingSpanStatus
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var child types.DocumentProcessingSpan
		if err := tx.Select("parent_span_id").First(&child, "id = ?", childSpanID).Error; err != nil {
			return err
		}
		if child.ParentSpanID == nil || *child.ParentSpanID == uuid.Nil {
			return nil
		}
		type aggregate struct {
			Active    int64
			Failed    int64
			Cancelled int64
		}
		var counts aggregate
		if err := tx.Model(&types.DocumentProcessingSpan{}).
			Select("COUNT(*) FILTER (WHERE status IN ('queued','running')) AS active, COUNT(*) FILTER (WHERE status = 'failed') AS failed, COUNT(*) FILTER (WHERE status = 'cancelled') AS cancelled").
			Where("parent_span_id = ?", *child.ParentSpanID).Scan(&counts).Error; err != nil {
			return err
		}
		status, code, reason := aggregateDisplayStatus(counts.Active, counts.Failed, counts.Cancelled)
		if status == types.ProcessingSpanRunning {
			parentStatus = status
			return nil
		}
		now := time.Now().UTC()
		if err := tx.Model(&types.DocumentProcessingSpan{}).
			Where("id = ? AND status IN ?", *child.ParentSpanID, []types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).
			Updates(map[string]interface{}{"status": status, "error_code": code, "error_reason": reason, "finished_at": now}).Error; err != nil {
			return err
		}
		var parent types.DocumentProcessingSpan
		if err := tx.Select("status").First(&parent, "id = ?", *child.ParentSpanID).Error; err != nil {
			return err
		}
		parentStatus = parent.Status
		return nil
	})
	return parentStatus, err
}

func aggregateDisplayStatus(active, failed, cancelled int64) (types.ProcessingSpanStatus, string, string) {
	if active > 0 {
		return types.ProcessingSpanRunning, "", ""
	}
	if cancelled > 0 {
		return types.ProcessingSpanCancelled, types.ProcessingErrorCancelled, "one or more workflow items were cancelled"
	}
	if failed > 0 {
		return types.ProcessingSpanFailed, types.ProcessingErrorChildFailed, "one or more workflow items failed"
	}
	return types.ProcessingSpanSucceeded, "", ""
}

var _ interfaces.DocumentProcessingSpanRepository = (*documentProcessingSpanRepository)(nil)
