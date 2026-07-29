package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"gorm.io/gorm"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

type taskDeadLetterRepository struct{ db *gorm.DB }

func NewTaskDeadLetterRepository(db *gorm.DB) interfaces.TaskDeadLetterRepository {
	return &taskDeadLetterRepository{db: db}
}

func deadLetterLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func cursorID(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	return strconv.ParseUint(cursor, 10, 64)
}

func (r *taskDeadLetterRepository) ListByScope(ctx context.Context, scope, scopeID, cursor string, limit int) ([]*types.TaskDeadLetter, string, error) {
	return r.list(ctx, func(q *gorm.DB) *gorm.DB { return q.Where("scope = ? AND scope_id = ?", scope, scopeID) }, cursor, limit)
}

func (r *taskDeadLetterRepository) ListByTaskType(ctx context.Context, taskType, cursor string, limit int) ([]*types.TaskDeadLetter, string, error) {
	return r.list(ctx, func(q *gorm.DB) *gorm.DB { return q.Where("task_type = ?", taskType) }, cursor, limit)
}

func (r *taskDeadLetterRepository) list(ctx context.Context, filter func(*gorm.DB) *gorm.DB, cursor string, limit int) ([]*types.TaskDeadLetter, string, error) {
	limit = deadLetterLimit(limit)
	id, err := cursorID(cursor)
	if err != nil {
		return nil, "", err
	}
	q := filter(r.db.WithContext(ctx).Model(&types.TaskDeadLetter{}))
	if id > 0 {
		q = q.Where("id < ?", id)
	}
	var rows []*types.TaskDeadLetter
	if err := q.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		logger.Error(ctx, "list task dead letters failed", "error", err)
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatUint(rows[len(rows)-1].ID, 10)
	}
	return rows, next, nil
}

func (r *taskDeadLetterRepository) DeleteByID(ctx context.Context, id int64) error {
	err := r.db.WithContext(ctx).Delete(&types.TaskDeadLetter{}, id).Error
	if err != nil {
		logger.Error(ctx, "delete task dead letter failed", "id", id, "error", err)
		return err
	}
	logger.Info(ctx, "task dead letter deleted", "id", id)
	return nil
}

func (r *taskDeadLetterRepository) Insert(ctx context.Context, dl *types.TaskDeadLetter) error {
	err := r.db.WithContext(ctx).Create(dl).Error
	if err != nil {
		logger.Error(ctx, "insert task dead letter failed", "task_type", dl.TaskType, "error", err)
		return err
	}
	logger.Info(ctx, "task dead letter recorded", "task_type", dl.TaskType, "scope", dl.Scope, "scope_id", dl.ScopeID, "last_error", dl.LastError)
	return nil
}

func (r *taskDeadLetterRepository) LatestFailureAt(
	ctx context.Context,
	scope string,
	scopeID string,
	taskType string,
) (*time.Time, error) {
	var row types.TaskDeadLetter
	err := r.db.WithContext(ctx).
		Where("scope = ? AND scope_id = ? AND task_type = ?", scope, scopeID, taskType).
		Order("failed_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		logger.Error(ctx, "get latest task dead letter failed",
			"scope", scope,
			"scope_id", scopeID,
			"task_type", taskType,
			"error", err)
		return nil, err
	}
	failedAt := row.FailedAt
	return &failedAt, nil
}
