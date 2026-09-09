package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	processingRunErrorCodeLimit   = 128
	processingRunErrorReasonLimit = 512
	processingRunKeyLimit         = 512
)

type processingRunRepository struct{ db *gorm.DB }

func NewProcessingRunRepository(db *gorm.DB) interfaces.ProcessingRunRepository {
	return &processingRunRepository{db: db}
}

func (r *processingRunRepository) CreateQueued(ctx context.Context, run types.ProcessingRun) (types.ProcessingRun, bool, error) {
	if err := validateProcessingRunCreate(run); err != nil {
		return types.ProcessingRun{}, false, err
	}
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	run.IdempotencyKey = strings.TrimSpace(run.IdempotencyKey)
	run.WorkflowKind = types.ProcessingWorkflowKind(strings.TrimSpace(string(run.WorkflowKind)))
	run.State = types.ProcessingRunQueued
	run.CancelRequestedAt, run.StartedAt, run.TerminalAt = nil, nil, nil
	run.ErrorCode, run.ErrorReason = "", ""
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "owner_document_id"}, {Name: "workflow_kind"}, {Name: "idempotency_key"}},
		DoNothing: true,
	}).Create(&run)
	if result.Error != nil {
		logger.Error(ctx, "create processing run failed", "document_id", run.OwnerDocumentID, "workflow", run.WorkflowKind, "error", result.Error)
		return types.ProcessingRun{}, false, result.Error
	}
	created := result.RowsAffected > 0
	if !created {
		stored, err := r.getByIdempotency(ctx, run.OwnerDocumentID, run.WorkflowKind, run.IdempotencyKey)
		return stored, false, err
	}
	logger.Info(ctx, "processing run queued", "run_id", run.ID, "document_id", run.OwnerDocumentID, "workflow", run.WorkflowKind)
	return run, true, nil
}

func (r *processingRunRepository) Get(ctx context.Context, id uuid.UUID) (types.ProcessingRun, error) {
	if id == uuid.Nil {
		return types.ProcessingRun{}, sql.ErrNoRows
	}
	var run types.ProcessingRun
	err := repositoryError(r.db.WithContext(ctx).First(&run, "id = ?", id).Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get processing run failed", "run_id", id, "error", err)
	}
	return run, err
}

func (r *processingRunRepository) TryStart(ctx context.Context, id uuid.UUID) (types.ProcessingRun, bool, error) {
	now := time.Now().UTC()
	return r.transition(ctx, id, []types.ProcessingRunState{types.ProcessingRunQueued}, types.ProcessingRunRunning, map[string]interface{}{
		"started_at":   gorm.Expr("COALESCE(started_at, ?)", now),
		"terminal_at":  nil,
		"error_code":   "",
		"error_reason": "",
	}, "start")
}

func (r *processingRunRepository) MarkRetryQueued(ctx context.Context, id uuid.UUID, code, reason string) (types.ProcessingRun, bool, error) {
	return r.transition(ctx, id, []types.ProcessingRunState{types.ProcessingRunRunning}, types.ProcessingRunQueued, map[string]interface{}{
		"error_code":   boundedProcessingRunText(code, processingRunErrorCodeLimit),
		"error_reason": boundedProcessingRunText(reason, processingRunErrorReasonLimit),
	}, "queue retry for")
}

func (r *processingRunRepository) RequestCancel(ctx context.Context, id uuid.UUID, code, reason string) (types.ProcessingRun, bool, error) {
	now := time.Now().UTC()
	return r.transition(ctx, id, []types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}, types.ProcessingRunCancelRequested, map[string]interface{}{
		"cancel_requested_at": gorm.Expr("COALESCE(cancel_requested_at, ?)", now),
		"error_code":          boundedProcessingRunText(code, processingRunErrorCodeLimit),
		"error_reason":        boundedProcessingRunText(reason, processingRunErrorReasonLimit),
	}, "request cancellation for")
}

func (r *processingRunRepository) RequestCancelActive(
	ctx context.Context, ownerDocumentID uuid.UUID, kind types.ProcessingWorkflowKind, code, reason string,
) ([]types.ProcessingRun, error) {
	if ownerDocumentID == uuid.Nil {
		return []types.ProcessingRun{}, nil
	}
	code = boundedProcessingRunText(code, processingRunErrorCodeLimit)
	reason = boundedProcessingRunText(reason, processingRunErrorReasonLimit)
	runs := []types.ProcessingRun{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("owner_document_id = ? AND workflow_kind = ? AND state IN ?", ownerDocumentID, kind,
				[]types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning, types.ProcessingRunCancelRequested}).
			Order("created_at ASC, id ASC").Find(&runs).Error; err != nil {
			return err
		}
		if len(runs) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, 0, len(runs))
		for _, run := range runs {
			ids = append(ids, run.ID)
		}
		now := time.Now().UTC()
		if err := tx.Model(&types.ProcessingRun{}).
			Where("id IN ? AND state IN ?", ids, []types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}).
			Updates(map[string]interface{}{
				"state":               types.ProcessingRunCancelRequested,
				"cancel_requested_at": gorm.Expr("COALESCE(cancel_requested_at, ?)", now),
				"error_code":          code, "error_reason": reason, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.DocumentProcessingSpan{}).
			Where("run_id IN ? AND status IN ?", ids,
				[]types.ProcessingSpanStatus{types.ProcessingSpanQueued, types.ProcessingSpanRunning}).
			Updates(map[string]interface{}{
				"status": types.ProcessingSpanCancelled, "error_code": code,
				"error_reason": reason, "finished_at": now, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.ProcessingRunItem{}).
			Where("run_id IN ? AND state IN ?", ids,
				[]types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}).
			Updates(map[string]interface{}{
				"state": types.ProcessingRunCancelled, "error_code": code,
				"error_reason": reason, "terminal_at": now, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		runs = []types.ProcessingRun{}
		return tx.Where("id IN ?", ids).Order("created_at ASC, id ASC").Find(&runs).Error
	})
	if err != nil {
		logger.Error(ctx, "request active processing run cancellation failed", "document_id", ownerDocumentID, "workflow", kind, "error", err)
		return nil, err
	}
	if len(runs) > 0 {
		logger.Info(ctx, "active processing runs cancellation requested", "document_id", ownerDocumentID, "workflow", kind, "run_count", len(runs))
	}
	return runs, nil
}

func (r *processingRunRepository) Finish(ctx context.Context, id uuid.UUID, state types.ProcessingRunState, code, reason string) (types.ProcessingRun, bool, error) {
	var from []types.ProcessingRunState
	switch state {
	case types.ProcessingRunSucceeded:
		from = []types.ProcessingRunState{types.ProcessingRunRunning}
	case types.ProcessingRunFailed:
		from = []types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}
	case types.ProcessingRunCancelled:
		from = []types.ProcessingRunState{types.ProcessingRunCancelRequested}
	default:
		return types.ProcessingRun{}, false, fmt.Errorf("processing run finish state %q is not terminal", state)
	}
	now := time.Now().UTC()
	updates := map[string]interface{}{
		"terminal_at":  now,
		"error_code":   boundedProcessingRunText(code, processingRunErrorCodeLimit),
		"error_reason": boundedProcessingRunText(reason, processingRunErrorReasonLimit),
	}
	if state == types.ProcessingRunSucceeded {
		updates["error_code"], updates["error_reason"] = "", ""
	}
	return r.transition(ctx, id, from, state, updates, "finish")
}

func (r *processingRunRepository) ListActive(ctx context.Context, ownerDocumentID uuid.UUID, kinds ...types.ProcessingWorkflowKind) ([]types.ProcessingRun, error) {
	if ownerDocumentID == uuid.Nil {
		return []types.ProcessingRun{}, nil
	}
	query := r.db.WithContext(ctx).Where("owner_document_id = ? AND state IN ?", ownerDocumentID, []types.ProcessingRunState{
		types.ProcessingRunQueued, types.ProcessingRunRunning, types.ProcessingRunCancelRequested,
	})
	if len(kinds) > 0 {
		query = query.Where("workflow_kind IN ?", kinds)
	}
	runs := []types.ProcessingRun{}
	if err := query.Order("created_at ASC, id ASC").Find(&runs).Error; err != nil {
		logger.Error(ctx, "list active processing runs failed", "document_id", ownerDocumentID, "error", err)
		return nil, err
	}
	return runs, nil
}

func (r *processingRunRepository) ListRecent(ctx context.Context, ownerDocumentID uuid.UUID, limit int) ([]types.ProcessingRun, error) {
	if ownerDocumentID == uuid.Nil {
		return []types.ProcessingRun{}, nil
	}
	if limit < 1 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	runs := []types.ProcessingRun{}
	err := r.db.WithContext(ctx).Where("owner_document_id = ?", ownerDocumentID).
		Order("CASE WHEN state IN ('queued','running','cancel_requested') THEN 0 ELSE 1 END, updated_at DESC, id DESC").
		Limit(limit).Find(&runs).Error
	if err != nil {
		logger.Error(ctx, "list recent processing runs failed", "document_id", ownerDocumentID, "error", err)
	}
	return runs, err
}

func (r *processingRunRepository) LatestAttempt(
	ctx context.Context, ownerDocumentID uuid.UUID, kind types.ProcessingWorkflowKind,
) (int64, error) {
	var latest int64
	err := r.db.WithContext(ctx).Model(&types.ProcessingRun{}).
		Where("owner_document_id = ? AND workflow_kind = ?", ownerDocumentID, kind).
		Select("COALESCE(MAX(attempt), 0)").Scan(&latest).Error
	if err != nil {
		logger.Error(ctx, "load latest processing run attempt failed", "document_id", ownerDocumentID, "workflow", kind, "error", err)
	}
	return latest, err
}

func (r *processingRunRepository) RegisterItem(
	ctx context.Context, runID uuid.UUID, itemKey string,
) (types.ProcessingRunItem, bool, error) {
	itemKey = strings.TrimSpace(itemKey)
	if runID == uuid.Nil || itemKey == "" {
		return types.ProcessingRunItem{}, false, fmt.Errorf("processing run item identity is required")
	}
	if len(itemKey) > 512 {
		return types.ProcessingRunItem{}, false, fmt.Errorf("processing run item key is too long")
	}
	item := types.ProcessingRunItem{RunID: runID, ItemKey: itemKey, State: types.ProcessingRunQueued}
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run types.ProcessingRun
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&run, "id = ?", runID).Error; err != nil {
			return err
		}
		if run.ItemsSealedAt != nil || (run.State != types.ProcessingRunQueued && run.State != types.ProcessingRunRunning) {
			return fmt.Errorf("processing run %s does not accept work items in state %s", runID, run.State)
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		return tx.First(&item, "run_id = ? AND item_key = ?", runID, itemKey).Error
	})
	if err != nil {
		logger.Error(ctx, "register processing run item failed", "run_id", runID, "item_key", itemKey, "error", err)
	} else if created {
		logger.Info(ctx, "processing run item registered", "run_id", runID, "item_key", itemKey)
	}
	return item, created, err
}

func (r *processingRunRepository) TryStartItem(
	ctx context.Context, runID uuid.UUID, itemKey string,
) (types.ProcessingRunItem, bool, error) {
	now := time.Now().UTC()
	var item types.ProcessingRunItem
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run types.ProcessingRun
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("state").First(&run, "id = ?", runID).Error; err != nil {
			return err
		}
		if run.State != types.ProcessingRunRunning {
			return tx.First(&item, "run_id = ? AND item_key = ?", runID, itemKey).Error
		}
		result := tx.Model(&types.ProcessingRunItem{}).
			Where("run_id = ? AND item_key = ? AND state = ?", runID, itemKey, types.ProcessingRunQueued).
			Updates(map[string]interface{}{"state": types.ProcessingRunRunning, "started_at": gorm.Expr("COALESCE(started_at, ?)", now), "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		applied = result.RowsAffected == 1
		return tx.First(&item, "run_id = ? AND item_key = ?", runID, itemKey).Error
	})
	err = repositoryError(err)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "start processing run item failed", "run_id", runID, "item_key", itemKey, "error", err)
	} else if applied {
		logger.Info(ctx, "processing run item started", "run_id", runID, "item_key", itemKey)
	}
	return item, applied, err
}

func (r *processingRunRepository) MarkItemRetry(ctx context.Context, runID uuid.UUID, itemKey string) error {
	err := r.db.WithContext(ctx).Model(&types.ProcessingRunItem{}).
		Where("run_id = ? AND item_key = ? AND state = ?", runID, itemKey, types.ProcessingRunRunning).
		Updates(map[string]interface{}{"state": types.ProcessingRunQueued, "updated_at": time.Now().UTC()}).Error
	if err != nil {
		logger.Error(ctx, "queue processing run item retry failed", "run_id", runID, "item_key", itemKey, "error", err)
	}
	return err
}

func (r *processingRunRepository) FinishItem(
	ctx context.Context, runID uuid.UUID, itemKey string, state types.ProcessingRunState, code, reason string,
) (types.ProcessingRun, error) {
	if state != types.ProcessingRunSucceeded && state != types.ProcessingRunFailed && state != types.ProcessingRunCancelled {
		return types.ProcessingRun{}, fmt.Errorf("processing run item terminal state %q is invalid", state)
	}
	var run types.ProcessingRun
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lockedRun types.ProcessingRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&lockedRun, "id = ?", runID).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.ProcessingRunItem{}).
			Where("run_id = ? AND item_key = ? AND state IN ?", runID, itemKey,
				[]types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}).
			Updates(map[string]interface{}{
				"state": state, "error_code": boundedProcessingRunText(code, processingRunErrorCodeLimit),
				"error_reason": boundedProcessingRunText(reason, processingRunErrorReasonLimit),
				"terminal_at":  now, "updated_at": now,
			}).Error; err != nil {
			return err
		}
		return r.finalizeSealedItems(tx, runID, &run)
	})
	if err != nil {
		logger.Error(ctx, "finish processing run item failed", "run_id", runID, "item_key", itemKey, "state", state, "error", err)
	} else {
		logger.Info(ctx, "processing run item finished", "run_id", runID, "item_key", itemKey, "state", state)
	}
	return run, err
}

func (r *processingRunRepository) SealItems(ctx context.Context, runID uuid.UUID) (types.ProcessingRun, error) {
	var run types.ProcessingRun
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&types.ProcessingRun{}).
			Where("id = ? AND items_sealed_at IS NULL AND state IN ?", runID,
				[]types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}).
			Updates(map[string]interface{}{"items_sealed_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return r.finalizeSealedItems(tx, runID, &run)
	})
	if err != nil {
		logger.Error(ctx, "seal processing run items failed", "run_id", runID, "error", err)
	} else {
		logger.Info(ctx, "processing run items sealed", "run_id", runID, "state", run.State)
	}
	return run, err
}

func (r *processingRunRepository) finalizeSealedItems(tx *gorm.DB, runID uuid.UUID, run *types.ProcessingRun) error {
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(run, "id = ?", runID).Error; err != nil {
		return err
	}
	if run.ItemsSealedAt == nil || (run.State != types.ProcessingRunQueued && run.State != types.ProcessingRunRunning) {
		return nil
	}
	type itemCounts struct{ Total, Active, Failed, Cancelled int64 }
	var counts itemCounts
	if err := tx.Model(&types.ProcessingRunItem{}).
		Select("COUNT(*) AS total, COUNT(*) FILTER (WHERE state IN ('queued','running')) AS active, COUNT(*) FILTER (WHERE state = 'failed') AS failed, COUNT(*) FILTER (WHERE state = 'cancelled') AS cancelled").
		Where("run_id = ?", runID).Scan(&counts).Error; err != nil {
		return err
	}
	if counts.Active > 0 {
		return nil
	}
	state, code, reason := types.ProcessingRunSucceeded, "", ""
	if counts.Failed > 0 || counts.Cancelled > 0 {
		state, code, reason = types.ProcessingRunFailed, "CHILD_FAILED", "one or more workflow items failed"
	}
	now := time.Now().UTC()
	if err := tx.Model(&types.ProcessingRun{}).Where("id = ? AND state IN ?", runID,
		[]types.ProcessingRunState{types.ProcessingRunQueued, types.ProcessingRunRunning}).
		Updates(map[string]interface{}{
			"state": state, "error_code": code, "error_reason": reason,
			"terminal_at": now, "updated_at": now,
		}).Error; err != nil {
		return err
	}
	return tx.First(run, "id = ?", runID).Error
}

func (r *processingRunRepository) transition(ctx context.Context, id uuid.UUID, from []types.ProcessingRunState, to types.ProcessingRunState, updates map[string]interface{}, action string) (types.ProcessingRun, bool, error) {
	if id == uuid.Nil {
		return types.ProcessingRun{}, false, sql.ErrNoRows
	}
	updates["state"] = to
	updates["updated_at"] = time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&types.ProcessingRun{}).Where("id = ? AND state IN ?", id, from).Updates(updates)
	if result.Error != nil {
		logger.Error(ctx, action+" processing run failed", "run_id", id, "state", to, "error", result.Error)
		return types.ProcessingRun{}, false, result.Error
	}
	run, err := r.Get(ctx, id)
	if err != nil {
		return types.ProcessingRun{}, false, err
	}
	if result.RowsAffected > 0 {
		logger.Info(ctx, "processing run state changed", "run_id", id, "state", to)
	}
	return run, result.RowsAffected > 0, nil
}

func (r *processingRunRepository) getByIdempotency(ctx context.Context, owner uuid.UUID, kind types.ProcessingWorkflowKind, key string) (types.ProcessingRun, error) {
	var run types.ProcessingRun
	err := repositoryError(r.db.WithContext(ctx).Where("owner_document_id = ? AND workflow_kind = ? AND idempotency_key = ?", owner, kind, key).First(&run).Error)
	return run, err
}

func validateProcessingRunCreate(run types.ProcessingRun) error {
	key := strings.TrimSpace(run.IdempotencyKey)
	switch {
	case run.OwnerDocumentID == uuid.Nil:
		return errors.New("processing run owner document id is required")
	case strings.TrimSpace(string(run.WorkflowKind)) == "":
		return errors.New("processing run workflow kind is required")
	case utf8.RuneCountInString(string(run.WorkflowKind)) > 64:
		return errors.New("processing run workflow kind exceeds 64 characters")
	case key == "":
		return errors.New("processing run idempotency key is required")
	case utf8.RuneCountInString(key) > processingRunKeyLimit:
		return fmt.Errorf("processing run idempotency key exceeds %d characters", processingRunKeyLimit)
	case run.Attempt < 1:
		return errors.New("processing run attempt must be positive")
	default:
		return nil
	}
}

func boundedProcessingRunText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

var _ interfaces.ProcessingRunRepository = (*processingRunRepository)(nil)
