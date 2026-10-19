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

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// mergeReferenceMetadata applies incoming citation metadata to one bibliography
// row. Resolution state and citing ownership are deliberately untouched.
func mergeReferenceMetadata(
	existing types.PaperReference,
	incoming types.PaperReference,
	patch *types.PaperReferenceMetadataPatch,
) types.PaperReference {
	if existing.Authors == nil {
		existing.Authors = []types.PaperAuthor{}
	}
	source := incoming.EffectiveMetadataSource()
	if patch != nil && patch.MetadataSource != nil {
		source = *patch.MetadataSource
	}
	var effective types.PaperReferenceMetadataPatch
	if patch != nil {
		effective = *patch
	}
	patch = &effective
	existingSource := existing.EffectiveMetadataSource()
	overwrite := referenceSourceRank(source) >= referenceSourceRank(existingSource)

	setString := func(dst *string, value *string) {
		if value == nil {
			return
		}
		trimmed := strings.TrimSpace(*value)
		if trimmed == "" {
			return
		}
		if *dst == "" || overwrite {
			*dst = trimmed
		}
	}
	// Values carried directly on the incoming row behave like a lower-priority
	// patch; the explicit patch (extraction or catalog) is applied after.
	base := incoming
	if patch.DOI == nil && base.DOI != "" {
		patch.DOI = &base.DOI
	}
	if patch.Title == nil && base.Title != "" {
		patch.Title = &base.Title
	}
	if patch.Venue == nil && base.Venue != "" {
		patch.Venue = &base.Venue
	}
	if patch.Volume == nil && base.Volume != "" {
		patch.Volume = &base.Volume
	}
	if patch.Issue == nil && base.Issue != "" {
		patch.Issue = &base.Issue
	}
	if patch.Pages == nil && base.Pages != "" {
		patch.Pages = &base.Pages
	}
	if patch.SourceURL == nil && base.SourceURL != "" {
		patch.SourceURL = &base.SourceURL
	}
	if patch.Authors == nil && len(base.Authors) > 0 {
		authors := base.Authors
		patch.Authors = &authors
	}
	if patch.PublishedAt == nil && base.PublishedAt != nil {
		patch.PublishedAt = base.PublishedAt
	}

	setString(&existing.DOI, patch.DOI)
	if existing.DOI != "" {
		existing.DOI = strings.ToLower(existing.DOI)
	}
	setString(&existing.Title, patch.Title)
	setString(&existing.Venue, patch.Venue)
	setString(&existing.Volume, patch.Volume)
	setString(&existing.Issue, patch.Issue)
	setString(&existing.Pages, patch.Pages)
	setString(&existing.SourceURL, patch.SourceURL)
	if patch.Authors != nil && len(*patch.Authors) > 0 && (len(existing.Authors) == 0 || overwrite) {
		existing.Authors = *patch.Authors
	}
	if patch.PublishedAt != nil && (existing.PublishedAt == nil || overwrite) {
		existing.PublishedAt = patch.PublishedAt
	}
	if existing.RawReference == "" && base.RawReference != "" {
		existing.RawReference = base.RawReference
	}
	if source != "" && (existingSource == "" || overwrite) {
		existing.MetadataSource = source
	}
	existing.UpdatedAt = time.Now().UTC()
	return existing
}

func applyReferenceMetadata(
	reference types.PaperReference, patch *types.PaperReferenceMetadataPatch,
) types.PaperReference {
	if patch == nil || !patch.HasChanges() {
		return reference
	}
	incoming := types.PaperReference{
		DOI:            derefString(patch.DOI),
		Title:          derefString(patch.Title),
		Venue:          derefString(patch.Venue),
		Volume:         derefString(patch.Volume),
		Issue:          derefString(patch.Issue),
		Pages:          derefString(patch.Pages),
		SourceURL:      derefString(patch.SourceURL),
		PublishedAt:    patch.PublishedAt,
		MetadataSource: reference.MetadataSource,
	}
	if patch.Authors != nil {
		incoming.Authors = *patch.Authors
	}
	if patch.MetadataSource != nil {
		incoming.MetadataSource = *patch.MetadataSource
	}
	return mergeReferenceMetadata(reference, incoming, patch)
}

func nullableReferenceError(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func referenceSourceRank(source types.PaperMetadataSource) int {
	switch source {
	case types.PaperMetadataSourceManual:
		return 3
	case types.PaperMetadataSourceCatalog:
		return 2
	case types.PaperMetadataSourceExtraction:
		return 1
	default:
		return 0
	}
}

func normalizeReferenceSlice(reference *types.PaperReference) {
	if reference.Authors == nil {
		reference.Authors = []types.PaperAuthor{}
	}
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

// UpdateReferenceSource applies a manual edit under a row lock. The row is
// marked manual-origin so a later extraction reconcile neither overwrites nor
// deletes the user's correction. Editing an unresolved reference also bumps
// the resolution generation and returns it to not_started, so an in-flight
// provider result can no longer apply stale metadata or a stale link.
func (r *paperCitationRepository) UpdateReferenceSource(
	ctx context.Context,
	id uuid.UUID,
	edit types.PaperReferenceSourceEdit,
) (types.PaperReference, error) {
	if id == uuid.Nil {
		return types.PaperReference{}, fmt.Errorf("%w: id is required", types.ErrInvalidPaperReference)
	}
	var result types.PaperReference
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var citation types.PaperCitation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&citation, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return sql.ErrNoRows
			}
			return err
		}
		now := time.Now().UTC()
		if edit.DOI != nil {
			citation.DOI = *edit.DOI
		}
		if edit.Title != nil {
			citation.Title = *edit.Title
		}
		if edit.Authors != nil {
			citation.Authors = *edit.Authors
			if citation.Authors == nil {
				citation.Authors = []types.PaperAuthor{}
			}
		}
		if edit.Venue != nil {
			citation.Venue = *edit.Venue
		}
		if edit.PublishedAtSet {
			citation.PublishedAt = edit.PublishedAt
		}
		if edit.Volume != nil {
			citation.Volume = *edit.Volume
		}
		if edit.Issue != nil {
			citation.Issue = *edit.Issue
		}
		if edit.Pages != nil {
			citation.Pages = *edit.Pages
		}
		if edit.SourceURL != nil {
			citation.SourceURL = *edit.SourceURL
		}
		citation.MetadataSource = types.PaperMetadataSourceManual
		citation.Origin = types.PaperCitationOriginManual
		if citation.CitedDocumentID == nil {
			citation.ResolutionGeneration++
			citation.ResolutionStatus = types.DocumentStageNotStarted
			citation.ResolutionProvider = ""
			citation.ResolutionErrorCode = nil
			citation.ResolutionErrorMsg = nil
			citation.ResolutionUpdatedAt = now
		}
		citation.UpdatedAt = now
		if err := tx.Save(&citation).Error; err != nil {
			return err
		}
		result = citation.PaperReference
		return nil
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Error(ctx, "update paper reference source failed", "reference_id", id, "error", err)
		}
		return types.PaperReference{}, err
	}
	normalizeReferenceSlice(&result)
	return result, nil
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
		// Serialize duplicate explicit requests under the row lock. A failed
		// attempt records an error and is retryable immediately; a pending
		// request is rejected until it succeeds, fails, or its in-flight window
		// expires (the worker may have died without a terminal write).
		failed := reference.DownloadErrorCode != nil || reference.DownloadErrorMessage != nil
		if reference.DownloadRequestedAt != nil && !failed &&
			time.Since(*reference.DownloadRequestedAt) < types.ReferenceDownloadInFlightWindow {
			return types.ErrReferenceDownloadInProgress
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
		if !errors.Is(err, types.ErrReferenceDownloadInProgress) {
			logger.Error(ctx, "queue reference download failed", "reference_id", id, "error", err)
		}
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
