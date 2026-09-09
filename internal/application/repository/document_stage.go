package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type documentStageColumns struct {
	status       string
	generation   string
	updatedAt    string
	errorCode    string
	errorMessage string
}

func (r *documentRepository) GetStage(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage,
) (types.DocumentStageState, error) {
	columns, err := columnsForDocumentStage(stage)
	if err != nil {
		return types.DocumentStageState{}, err
	}
	if documentID == uuid.Nil {
		return types.DocumentStageState{}, sql.ErrNoRows
	}
	state := types.DocumentStageState{Stage: stage}
	query := fmt.Sprintf(
		"SELECT %s AS status, %s AS generation, %s AS updated_at, %s AS error_code, %s AS error_message FROM documents WHERE id = ?",
		columns.status, columns.generation, columns.updatedAt, columns.errorCode, columns.errorMessage,
	)
	err = repositoryError(r.db.WithContext(ctx).Raw(query, documentID).Scan(&state).Error)
	if err == nil && state.Status == "" {
		err = sql.ErrNoRows
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		logger.Error(ctx, "get document stage failed", "document_id", documentID, "stage", stage, "error", err)
	}
	return state, err
}

func (r *documentRepository) QueueStageNewGeneration(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage,
) (types.DocumentStageState, bool, error) {
	columns, err := columnsForDocumentStage(stage)
	if err != nil {
		return types.DocumentStageState{}, false, err
	}
	if documentID == uuid.Nil {
		return types.DocumentStageState{}, false, sql.ErrNoRows
	}
	now := time.Now().UTC()
	query := fmt.Sprintf(`UPDATE documents
		SET %s = ?, %s = %s + 1, %s = ?, %s = NULL, %s = NULL
		WHERE id = ? AND source_state NOT IN ('deleting', 'merged') AND %s IN ?
		RETURNING %s AS status, %s AS generation, %s AS updated_at, %s AS error_code, %s AS error_message`, columns.status, columns.generation,
		columns.generation, columns.updatedAt, columns.errorCode, columns.errorMessage, columns.status, columns.status, columns.generation, columns.updatedAt, columns.errorCode, columns.errorMessage)
	return r.executeStageTransition(ctx, documentID, stage, query, "queue new document stage generation",
		types.DocumentStageQueued, now, documentID, []types.DocumentStageStatus{
			types.DocumentStageNotStarted, types.DocumentStageSucceeded, types.DocumentStageFailed,
			types.DocumentStageCancelled, types.DocumentStageSkipped,
		})
}

func (r *documentRepository) TryStartStage(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64,
) (types.DocumentStageState, bool, error) {
	return r.transitionStage(ctx, documentID, stage, generation,
		[]types.DocumentStageStatus{types.DocumentStageQueued}, types.DocumentStageRunning, "start document stage")
}

func (r *documentRepository) MarkStageRetryQueued(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64,
) (types.DocumentStageState, bool, error) {
	return r.transitionStage(ctx, documentID, stage, generation,
		[]types.DocumentStageStatus{types.DocumentStageRunning}, types.DocumentStageQueued, "queue document stage retry")
}

func (r *documentRepository) FinishStage(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64,
	status types.DocumentStageStatus, errorCode, errorMessage string,
) (types.DocumentStageState, bool, error) {
	var from []types.DocumentStageStatus
	switch status {
	case types.DocumentStageSucceeded, types.DocumentStageSkipped:
		from = []types.DocumentStageStatus{types.DocumentStageRunning}
	case types.DocumentStageFailed:
		from = []types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning}
	default:
		return types.DocumentStageState{}, false, fmt.Errorf("document stage finish status %q is not supported", status)
	}
	if len(errorCode) > 64 || len(errorMessage) > 512 {
		return types.DocumentStageState{}, false, errors.New("document stage error exceeds persisted bound")
	}
	return r.transitionStageWithError(ctx, documentID, stage, generation, from, status, errorCode, errorMessage, "finish document stage")
}

func (r *documentRepository) CancelStage(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64,
) (types.DocumentStageState, bool, error) {
	return r.transitionStage(ctx, documentID, stage, generation,
		[]types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning},
		types.DocumentStageCancelled, "cancel document stage")
}

func (r *documentRepository) AllowsStageWork(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64,
) (bool, error) {
	if generation < 1 {
		return false, nil
	}
	state, err := r.GetStage(ctx, documentID, stage)
	if err != nil {
		return false, err
	}
	return state.Generation == generation && state.Status == types.DocumentStageRunning, nil
}

func (r *documentRepository) transitionStage(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64,
	from []types.DocumentStageStatus, to types.DocumentStageStatus, action string,
) (types.DocumentStageState, bool, error) {
	columns, err := columnsForDocumentStage(stage)
	if err != nil {
		return types.DocumentStageState{}, false, err
	}
	if documentID == uuid.Nil {
		return types.DocumentStageState{}, false, sql.ErrNoRows
	}
	if generation < 1 {
		return types.DocumentStageState{}, false, errors.New("document stage generation must be positive")
	}
	now := time.Now().UTC()
	query := fmt.Sprintf(`UPDATE documents SET %s = ?, %s = ?, %s = NULL, %s = NULL
		WHERE id = ? AND source_state NOT IN ('deleting', 'merged') AND %s = ? AND %s IN ?
		RETURNING %s AS status, %s AS generation, %s AS updated_at, %s AS error_code, %s AS error_message`, columns.status, columns.updatedAt,
		columns.errorCode, columns.errorMessage, columns.generation, columns.status, columns.status, columns.generation, columns.updatedAt, columns.errorCode, columns.errorMessage)
	return r.executeStageTransition(ctx, documentID, stage, query, action, to, now, documentID, generation, from)
}

func (r *documentRepository) transitionStageWithError(ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, generation int64, from []types.DocumentStageStatus, to types.DocumentStageStatus, errorCode, errorMessage, action string) (types.DocumentStageState, bool, error) {
	columns, err := columnsForDocumentStage(stage)
	if err != nil {
		return types.DocumentStageState{}, false, err
	}
	if generation < 1 {
		return types.DocumentStageState{}, false, errors.New("document stage generation must be positive")
	}
	var code, message interface{}
	if errorCode != "" {
		code = errorCode
	}
	if errorMessage != "" {
		message = errorMessage
	}
	now := time.Now().UTC()
	query := fmt.Sprintf(`UPDATE documents SET %s = ?, %s = ?, %s = ?, %s = ?
		WHERE id = ? AND source_state NOT IN ('deleting', 'merged') AND %s = ? AND %s IN ?
		RETURNING %s AS status, %s AS generation, %s AS updated_at, %s AS error_code, %s AS error_message`, columns.status, columns.updatedAt, columns.errorCode, columns.errorMessage,
		columns.generation, columns.status, columns.status, columns.generation, columns.updatedAt, columns.errorCode, columns.errorMessage)
	return r.executeStageTransition(ctx, documentID, stage, query, action, to, now, code, message, documentID, generation, from)
}

func (r *documentRepository) executeStageTransition(
	ctx context.Context, documentID uuid.UUID, stage types.DocumentStage, query, action string, args ...interface{},
) (types.DocumentStageState, bool, error) {
	state := types.DocumentStageState{Stage: stage}
	result := r.db.WithContext(ctx).Raw(query, args...).Scan(&state)
	if result.Error != nil {
		logger.Error(ctx, action+" failed", "document_id", documentID, "stage", stage, "error", result.Error)
		return types.DocumentStageState{}, false, result.Error
	}
	applied := result.RowsAffected > 0
	if applied {
		logger.Info(ctx, "document stage state changed", "document_id", documentID, "stage", stage,
			"status", state.Status, "generation", state.Generation)
		return state, true, nil
	}
	state, err := r.GetStage(ctx, documentID, stage)
	return state, false, err
}

func columnsForDocumentStage(stage types.DocumentStage) (documentStageColumns, error) {
	switch stage {
	case types.DocumentStageMarkdown:
		return documentStageColumns{"markdown_status", "markdown_generation", "markdown_updated_at", "markdown_error_code", "markdown_error_message"}, nil
	case types.DocumentStageMetadataExtraction:
		return documentStageColumns{"metadata_extraction_status", "metadata_extraction_generation", "metadata_extraction_updated_at", "metadata_extraction_error_code", "metadata_extraction_error_message"}, nil
	case types.DocumentStageSourceLookup:
		return documentStageColumns{"source_lookup_status", "source_lookup_generation", "source_lookup_updated_at", "source_lookup_error_code", "source_lookup_error_message"}, nil
	case types.DocumentStageReferenceLookup:
		return documentStageColumns{"reference_lookup_status", "reference_lookup_generation", "reference_lookup_updated_at", "reference_lookup_error_code", "reference_lookup_error_message"}, nil
	default:
		return documentStageColumns{}, fmt.Errorf("invalid document stage %q", stage)
	}
}
