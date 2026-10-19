package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

func (r *paperCitationRepository) GetReference(ctx context.Context, id uuid.UUID) (types.PaperReference, error) {
	var reference types.PaperReference
	err := r.db.WithContext(ctx).First(&reference, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return types.PaperReference{}, sql.ErrNoRows
	}
	if err != nil {
		logger.Error(ctx, "get paper reference failed", "reference_id", id, "error", err)
		return types.PaperReference{}, err
	}
	normalizeReferenceSlice(&reference)
	return reference, nil
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
		manualKeys := make(map[string]struct{}, len(existing))
		for _, citation := range existing {
			if citation.EffectiveOrigin() == types.PaperCitationOriginManual {
				manualKeys[citation.ReferenceKey] = struct{}{}
			}
		}
		if err := tx.Where("citing_document_id = ? AND origin = ?", citingDocumentID,
			types.PaperCitationOriginExtraction).Delete(&types.PaperCitation{}).Error; err != nil {
			return err
		}
		toCreate := make([]types.PaperCitation, 0, len(automatic))
		for _, citation := range automatic {
			if _, manual := manualKeys[citation.ReferenceKey]; manual {
				continue
			}
			toCreate = append(toCreate, citation)
		}
		if len(toCreate) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&toCreate).Error; err != nil {
				return err
			}
		}
		items = make([]types.PaperCitation, 0, len(existing)+len(toCreate))
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

func (r *paperCitationRepository) ListByCitedDocumentID(
	ctx context.Context,
	citedDocumentID uuid.UUID,
) ([]types.PaperCitation, error) {
	citations := make([]types.PaperCitation, 0)
	err := r.db.WithContext(ctx).
		Where("cited_document_id = ?", citedDocumentID).
		Order("created_at DESC, citing_document_id").
		Find(&citations).Error
	if err != nil {
		logger.Error(ctx, "list citing papers failed",
			"cited_document_id", citedDocumentID,
			"error", err)
		return nil, err
	}
	for index := range citations {
		normalizeReferenceSlice(&citations[index].PaperReference)
	}
	return citations, nil
}

func (r *paperCitationRepository) QueueResolution(
	ctx context.Context, id uuid.UUID,
) (types.PaperReference, bool, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&types.PaperReference{}).
		Where("id = ? AND cited_document_id IS NULL AND resolution_status NOT IN ?", id,
			[]types.DocumentStageStatus{
				types.DocumentStageQueued, types.DocumentStageRunning, types.DocumentStageSucceeded,
			}).
		Updates(map[string]interface{}{
			"resolution_status":        types.DocumentStageQueued,
			"resolution_generation":    gorm.Expr("resolution_generation + 1"),
			"resolution_provider":      "",
			"resolution_error_code":    nil,
			"resolution_error_message": nil,
			"resolution_updated_at":    now,
		})
	if result.Error != nil {
		logger.Error(ctx, "queue paper reference resolution failed", "reference_id", id, "error", result.Error)
		return types.PaperReference{}, false, result.Error
	}
	reference, err := r.GetReference(ctx, id)
	if err != nil {
		return types.PaperReference{}, false, err
	}
	return reference, result.RowsAffected > 0, nil
}

func (r *paperCitationRepository) StartResolution(
	ctx context.Context, id uuid.UUID, generation int64,
) (types.PaperReference, bool, error) {
	if generation < 1 {
		return types.PaperReference{}, false, fmt.Errorf("%w: generation must be positive", types.ErrInvalidPaperReference)
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&types.PaperReference{}).
		Where("id = ? AND resolution_status = ? AND resolution_generation = ?", id, types.DocumentStageQueued, generation).
		Updates(map[string]interface{}{
			"resolution_status":        types.DocumentStageRunning,
			"resolution_updated_at":    now,
			"resolution_error_code":    nil,
			"resolution_error_message": nil,
		})
	if result.Error != nil {
		logger.Error(ctx, "start paper reference resolution failed", "reference_id", id, "generation", generation, "error", result.Error)
		return types.PaperReference{}, false, result.Error
	}
	reference, err := r.GetReference(ctx, id)
	if err != nil {
		return types.PaperReference{}, false, err
	}
	return reference, result.RowsAffected > 0, nil
}

func (r *paperCitationRepository) FinishResolution(
	ctx context.Context,
	id uuid.UUID,
	generation int64,
	status types.DocumentStageStatus,
	provider string,
	errorCode string,
	errorMessage string,
	update *types.PaperReferenceMetadataPatch,
	resolvedDocumentID *uuid.UUID,
) (types.PaperReference, bool, error) {
	if !status.Valid() || (status != types.DocumentStageQueued && !status.Terminal()) {
		return types.PaperReference{}, false, fmt.Errorf("unsupported reference resolution status %q", status)
	}
	if len(errorCode) > types.MaxPaperReferenceErrorCodeBytes || len(errorMessage) > types.MaxPaperReferenceErrorMessageBytes {
		return types.PaperReference{}, false, errors.New("reference resolution error exceeds persisted bound")
	}
	if generation < 1 {
		return types.PaperReference{}, false, fmt.Errorf("%w: generation must be positive", types.ErrInvalidPaperReference)
	}

	var result types.PaperReference
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reference types.PaperReference
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&reference, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		if reference.ResolutionGeneration != generation || reference.ResolutionStatus != types.DocumentStageRunning {
			result = reference
			return nil
		}
		if resolvedDocumentID != nil {
			if _, err := linkCitationDocumentTx(tx, &reference, *resolvedDocumentID, false); err != nil {
				return err
			}
		}
		reference = applyReferenceMetadata(reference, update)
		now := time.Now().UTC()
		reference.ResolutionStatus = status
		reference.ResolutionProvider = strings.TrimSpace(provider)
		reference.ResolutionErrorCode = nullableReferenceError(errorCode)
		reference.ResolutionErrorMsg = nullableReferenceError(errorMessage)
		reference.ResolutionUpdatedAt = now
		if status == types.DocumentStageQueued {
			reference.ResolutionErrorCode = nil
			reference.ResolutionErrorMsg = nil
		}
		reference.UpdatedAt = now
		if err := tx.Save(&reference).Error; err != nil {
			return err
		}
		result = reference
		applied = true
		return nil
	})
	if err != nil {
		logger.Error(ctx, "finish paper reference resolution failed", "reference_id", id, "generation", generation, "status", status, "error", err)
		return types.PaperReference{}, false, err
	}
	normalizeReferenceSlice(&result)
	return result, applied, nil
}

// QueueDownload starts one explicit reference-PDF attempt. A new attempt
// supersedes any previously recorded failure; an already-linked reference is
// never queued again.
func (r *paperCitationRepository) QueueDownload(
	ctx context.Context, id uuid.UUID, attempt uuid.UUID,
) (types.PaperReference, bool, error) {
	if attempt == uuid.Nil {
		return types.PaperReference{}, false, fmt.Errorf("%w: download attempt is required", types.ErrInvalidPaperReference)
	}
	var result types.PaperReference
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reference types.PaperReference
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&reference, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		if reference.CitedDocumentID != nil {
			result = reference
			return nil
		}
		now := time.Now().UTC()
		if err := tx.Model(&types.PaperReference{}).Where("id = ?", id).Updates(map[string]interface{}{
			"download_attempt":       attempt,
			"download_requested_at":  now,
			"download_error_code":    nil,
			"download_error_message": nil,
			"download_updated_at":    now,
			"updated_at":             now,
		}).Error; err != nil {
			return err
		}
		if err := tx.First(&reference, "id = ?", id).Error; err != nil {
			return err
		}
		result = reference
		applied = true
		return nil
	})
	if err != nil {
		logger.Error(ctx, "queue reference download failed", "reference_id", id, "error", err)
		return types.PaperReference{}, false, err
	}
	normalizeReferenceSlice(&result)
	return result, applied, nil
}

// FailDownload records a terminal download failure for one attempt. A stale
// attempt (a newer request replaced it) cannot overwrite the current state.
func (r *paperCitationRepository) FailDownload(
	ctx context.Context, id uuid.UUID, attempt uuid.UUID, errorCode, errorMessage string,
) (bool, error) {
	if attempt == uuid.Nil {
		return false, fmt.Errorf("%w: download attempt is required", types.ErrInvalidPaperReference)
	}
	errorCode = strings.TrimSpace(errorCode)
	errorMessage = strings.TrimSpace(errorMessage)
	if len(errorCode) > types.MaxPaperReferenceErrorCodeBytes || len(errorMessage) > types.MaxPaperReferenceErrorMessageBytes {
		return false, fmt.Errorf("%w: download error exceeds persisted bound", types.ErrInvalidPaperReference)
	}
	var code, message interface{}
	if errorCode != "" {
		code = errorCode
	}
	if errorMessage != "" {
		message = errorMessage
	}
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&types.PaperReference{}).
		Where("id = ? AND cited_document_id IS NULL AND download_attempt = ?", id, attempt).
		Updates(map[string]interface{}{
			"download_error_code":    code,
			"download_error_message": message,
			"download_updated_at":    now,
			"updated_at":             now,
		})
	if result.Error != nil {
		logger.Error(ctx, "record reference download failure failed", "reference_id", id, "error", result.Error)
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *paperCitationRepository) LinkResolvedDocument(
	ctx context.Context, id uuid.UUID, documentID uuid.UUID, overwrite bool,
) (types.PaperReference, bool, error) {
	if documentID == uuid.Nil {
		return types.PaperReference{}, false, fmt.Errorf("%w: document id is required", types.ErrInvalidPaperReference)
	}
	var result types.PaperReference
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reference types.PaperReference
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&reference, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		linked, err := linkCitationDocumentTx(tx, &reference, documentID, overwrite)
		if err != nil {
			return err
		}
		result = reference
		applied = linked
		return nil
	})
	if err != nil {
		logger.Error(ctx, "link paper reference document failed", "reference_id", id, "document_id", documentID, "error", err)
		return types.PaperReference{}, false, err
	}
	normalizeReferenceSlice(&result)
	return result, applied, nil
}

// linkCitationDocumentTx updates exactly one citation row. It never fans out to
// other citing papers because each bibliography entry owns its state.
func linkCitationDocumentTx(
	tx *gorm.DB, reference *types.PaperReference, documentID uuid.UUID, overwrite bool,
) (bool, error) {
	var document types.Document
	if err := tx.Select("id", "source_state").First(&document, "id = ?", documentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, sql.ErrNoRows
		}
		return false, err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return false, fmt.Errorf("%w: resolved document is not available", types.ErrInvalidPaperReference)
	}
	if reference.CitingDocumentID == documentID {
		return false, types.ErrSelfPaperCitation
	}
	if reference.CitedDocumentID != nil {
		if *reference.CitedDocumentID == documentID {
			return false, nil
		}
		if !overwrite {
			return false, nil
		}
	}
	now := time.Now().UTC()
	updates := map[string]interface{}{
		"cited_document_id":        documentID,
		"resolution_status":        types.DocumentStageSucceeded,
		"resolution_provider":      reference.ResolutionProvider,
		"resolution_error_code":    nil,
		"resolution_error_message": nil,
		"resolution_updated_at":    now,
		"updated_at":               now,
	}
	if reference.ResolutionStatus.Active() {
		updates["resolution_generation"] = gorm.Expr("resolution_generation + 1")
	}
	if err := tx.Model(&types.PaperReference{}).Where("id = ?", reference.ReferenceID).Updates(updates).Error; err != nil {
		return false, err
	}
	if err := tx.First(reference, "id = ?", reference.ReferenceID).Error; err != nil {
		return false, err
	}
	return true, nil
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
