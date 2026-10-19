package service

import (
	"context"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
)

// FailInterruptedCatalogWork applies the dispatcher's restart policy: queued
// or running source/reference catalog work is failed on process startup
// because the in-process queue and provider assignment state are gone.
func (h *DocumentHousekeepingService) FailInterruptedCatalogWork(ctx context.Context) error {
	if h == nil || h.db == nil {
		return nil
	}
	now := time.Now().UTC()
	message := "server restarted before catalog lookup completed"

	if err := h.db.WithContext(ctx).Exec(`UPDATE documents
		SET source_lookup_status = 'failed',
		    source_lookup_error_code = ?,
		    source_lookup_error_message = ?,
		    source_lookup_updated_at = ?
		WHERE source_state = 'available'
		  AND source_lookup_status IN ('queued','running')`,
		types.ProcessingErrorTaskFailed, message, now).Error; err != nil {
		return err
	}
	if err := h.db.WithContext(ctx).Exec(`UPDATE documents
		SET reference_lookup_status = 'failed',
		    reference_lookup_error_code = ?,
		    reference_lookup_error_message = ?,
		    reference_lookup_updated_at = ?
		WHERE source_state = 'available'
		  AND reference_lookup_status IN ('queued','running')`,
		types.ProcessingErrorTaskFailed, message, now).Error; err != nil {
		return err
	}
	if err := h.db.WithContext(ctx).Exec(`UPDATE paper_citations
		SET resolution_status = 'failed',
		    resolution_error_code = ?,
		    resolution_error_message = ?,
		    resolution_updated_at = ?,
		    updated_at = ?
		WHERE cited_document_id IS NULL
		  AND resolution_status IN ('queued','running')`,
		types.ProcessingErrorTaskFailed, message, now, now).Error; err != nil {
		return err
	}
	// Active observation spans belong to the process that just died. Mark
	// them cancelled so the processing view does not show forever-running
	// catalog work after restart.
	if err := h.db.WithContext(ctx).Exec(`UPDATE document_processing_spans
		SET status = 'cancelled',
		    error_code = ?,
		    error_reason = ?,
		    finished_at = ?,
		    updated_at = ?
		WHERE status IN ('queued','running')
		  AND kind IN ('catalog_source','catalog_reference','catalog_provider')`,
		types.ProcessingErrorCancelled, message, now, now).Error; err != nil {
		return err
	}
	logger.Info(ctx, "interrupted catalog work marked failed", "reason", "server restart")
	return nil
}
