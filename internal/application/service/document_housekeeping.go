package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DocumentHousekeepingService recovers document rows left queued or processing
// after their asynchronous work disappeared. It deliberately checks the task
// backend before marking a stale row failed so normal queue backpressure is
// never mistaken for an orphaned parse.
type DocumentHousekeepingService struct {
	db                   *gorm.DB
	inspector            interfaces.TaskInspector
	dispatcher           *CatalogDispatcher
	enabled              bool
	interval             time.Duration
	staleThreshold       time.Duration
	spanRetentionEnabled bool
	spanSuccessRetention time.Duration
	spanFailureRetention time.Duration
	spanRetentionBatch   int

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewDocumentHousekeepingService configures the periodic orphan recovery
// worker. Start is separate so dependency construction has no background side
// effects and the server owns its lifecycle.
func NewDocumentHousekeepingService(
	db *gorm.DB, cfg config.Config, inspector interfaces.TaskInspector,
	dispatcher *CatalogDispatcher,
) *DocumentHousekeepingService {
	interval := cfg.DocumentHousekeepingInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	staleThreshold := cfg.DocumentStaleThreshold
	if staleThreshold <= 0 {
		staleThreshold = time.Hour + 10*time.Minute
	}
	return &DocumentHousekeepingService{
		db: db, inspector: inspector, dispatcher: dispatcher,
		enabled:  cfg.DocumentHousekeepingEnabled,
		interval: interval, staleThreshold: staleThreshold,
		spanRetentionEnabled: cfg.ProcessingSpanRetentionEnabled,
		spanSuccessRetention: cfg.ProcessingSpanSuccessRetention,
		spanFailureRetention: cfg.ProcessingSpanFailureRetention,
		spanRetentionBatch:   cfg.ProcessingSpanRetentionBatch,
	}
}

// Start launches the periodic sweep. It is idempotent and does not run an
// immediate sweep: a newly started server should not classify old rows before
// the first configured interval has elapsed.
func (h *DocumentHousekeepingService) Start(ctx context.Context) error {
	if h == nil || h.db == nil {
		return fmt.Errorf("document housekeeping database is required")
	}
	if !h.enabled {
		logger.Info(ctx, "document housekeeping disabled")
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go h.run(runCtx, h.done)
	logger.Info(ctx, "document housekeeping started", "interval", h.interval.String(), "stale_threshold", h.staleThreshold.String())
	return nil
}

// Stop terminates the sweep and waits for its goroutine to exit. It is safe to
// call when Start was never called.
func (h *DocumentHousekeepingService) Stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	cancel, done := h.cancel, h.done
	h.cancel, h.done = nil, nil
	h.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (h *DocumentHousekeepingService) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Keep the runner context so Stop cancels an in-flight database
			// query instead of waiting for its per-sweep timeout to expire.
			h.runSweep(ctx)
		}
	}
}

// sweepTimeout bounds one whole housekeeping pass. It is intentionally much
// longer than a single DB statement: a pass may probe up to 200 candidates and
// a briefly slow Redis must delay classification, not turn it into a false
// positive.
const (
	sweepTimeout = 2 * time.Minute
	probeTimeout = 10 * time.Second
)

func (h *DocumentHousekeepingService) runSweep(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	cutoff := time.Now().UTC().Add(-h.staleThreshold)

	h.recoverStaleStages(sweepCtx, cutoff)
	h.recoverStaleReferences(sweepCtx, cutoff)
	h.cleanupTerminalSpans(sweepCtx)
	h.logPoolStats(ctx)
}

// logPoolStats keeps pool pressure visible without adding a metrics client.
// wait_count/wait_duration reveal worker and HTTP traffic queueing on the
// shared pool before latency turns into user-visible timeouts.
func (h *DocumentHousekeepingService) logPoolStats(ctx context.Context) {
	sqlDB, err := h.db.DB()
	if err != nil {
		return
	}
	stats := sqlDB.Stats()
	logger.Debug(ctx, "database pool stats",
		"open", stats.OpenConnections,
		"in_use", stats.InUse,
		"idle", stats.Idle,
		"max_open", stats.MaxOpenConnections,
		"wait_count", stats.WaitCount,
		"wait_duration_ms", stats.WaitDuration.Milliseconds(),
	)
}

type staleStageCandidate struct {
	DocumentID uuid.UUID
	Stage      types.DocumentStage
	Generation int64
}

func (h *DocumentHousekeepingService) recoverStaleStages(ctx context.Context, cutoff time.Time) {
	const candidateLimit = 200
	var candidates []staleStageCandidate
	query := `SELECT id AS document_id, 'markdown' AS stage, markdown_generation AS generation FROM documents WHERE source_state = 'available' AND markdown_status IN ('queued','running') AND markdown_updated_at < ?
		UNION ALL SELECT id, 'metadata_extraction', metadata_extraction_generation FROM documents WHERE source_state = 'available' AND metadata_extraction_status IN ('queued','running') AND metadata_extraction_updated_at < ?
		UNION ALL SELECT id, 'source_lookup', source_lookup_generation FROM documents WHERE source_state = 'available' AND source_lookup_status IN ('queued','running') AND source_lookup_updated_at < ?
		UNION ALL SELECT id, 'reference_lookup', reference_lookup_generation FROM documents WHERE source_state = 'available' AND reference_lookup_status IN ('queued','running') AND reference_lookup_updated_at < ?
		LIMIT ?`
	if err := h.db.WithContext(ctx).Raw(query, cutoff, cutoff, cutoff, cutoff, candidateLimit).Scan(&candidates).Error; err != nil {
		logger.Warn(ctx, "document stage housekeeping candidate query failed", "error", err)
		return
	}
	for _, candidate := range candidates {
		queued := false
		if h.inspector != nil {
			probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			var err error
			queued, err = h.inspector.HasQueuedTaskForStage(probeCtx, candidate.DocumentID.String(), candidate.Stage, candidate.Generation)
			cancel()
			if err != nil {
				// A transient Redis error is not evidence that work disappeared.
				// Keep the row untouched and retry on the next sweep.
				logger.Warn(ctx, "document stage queue probe failed; leaving candidate for next sweep",
					"document_id", candidate.DocumentID, "stage", candidate.Stage,
					"generation", candidate.Generation, "error", err)
				continue
			}
		}
		if queued {
			continue
		}
		stateColumn := stageStateColumn(candidate.Stage)
		if stateColumn == "" {
			continue
		}
		result := h.db.WithContext(ctx).Model(&types.Document{}).
			Where("id = ? AND source_state NOT IN ('deleting','merged') AND "+stateColumn+" IN ('queued','running') AND "+stageGenerationColumn(candidate.Stage)+" = ? AND "+stageUpdatedAtColumn(candidate.Stage)+" < ?", candidate.DocumentID, candidate.Generation, cutoff).
			Updates(map[string]interface{}{stateColumn: types.DocumentStageFailed, stageErrorCodeColumn(candidate.Stage): types.ProcessingErrorTaskFailed, stageErrorMessageColumn(candidate.Stage): "processing task became stale without queued work", stageUpdatedAtColumn(candidate.Stage): time.Now().UTC()})
		if result.Error != nil {
			logger.Warn(ctx, "document stage housekeeping update failed", "document_id", candidate.DocumentID, "stage", candidate.Stage, "error", result.Error)
			continue
		}
		if result.RowsAffected > 0 {
			logger.Info(ctx, "document stage recovered as stale", "document_id", candidate.DocumentID, "stage", candidate.Stage, "generation", candidate.Generation)
		}
	}
}

type staleReferenceCandidate struct {
	ReferenceID uuid.UUID
	Generation  int64
}

func (h *DocumentHousekeepingService) recoverStaleReferences(ctx context.Context, cutoff time.Time) {
	const candidateLimit = 200
	var candidates []staleReferenceCandidate
	if err := h.db.WithContext(ctx).Raw(`SELECT id AS reference_id, resolution_generation AS generation
		FROM paper_citations
		WHERE cited_document_id IS NULL
		  AND resolution_status IN ('queued','running')
		  AND resolution_updated_at < ?
		LIMIT ?`, cutoff, candidateLimit).Scan(&candidates).Error; err != nil {
		logger.Warn(ctx, "reference resolution housekeeping candidate query failed", "error", err)
		return
	}
	for _, candidate := range candidates {
		// Reference queries are process-local now; the dispatcher can answer
		// exactly whether this generation is still queued or executing.
		if h.dispatcher != nil && h.dispatcher.HasReference(candidate.ReferenceID, candidate.Generation) {
			continue
		}
		if h.inspector != nil {
			probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			queued, err := h.inspector.HasQueuedQueryApplyForReference(
				probeCtx, candidate.ReferenceID.String(), candidate.Generation,
			)
			cancel()
			if err != nil {
				logger.Warn(ctx, "reference result queue probe failed; leaving candidate for next sweep",
					"reference_id", candidate.ReferenceID, "generation", candidate.Generation, "error", err)
				continue
			}
			if queued {
				continue
			}
		}
		now := time.Now().UTC()
		result := h.db.WithContext(ctx).Exec(`UPDATE paper_citations
			SET resolution_status = 'failed', resolution_error_code = ?,
			    resolution_error_message = ?, resolution_updated_at = ?, updated_at = ?
			WHERE id = ? AND cited_document_id IS NULL
			  AND resolution_status IN ('queued','running')
			  AND resolution_generation = ? AND resolution_updated_at < ?`,
			types.ProcessingErrorTaskFailed, "reference resolution task became stale without queued work",
			now, now, candidate.ReferenceID, candidate.Generation, cutoff)
		if result.Error != nil {
			logger.Warn(ctx, "reference resolution housekeeping update failed", "reference_id", candidate.ReferenceID, "error", result.Error)
			continue
		}
		if result.RowsAffected > 0 {
			logger.Info(ctx, "reference resolution recovered as stale",
				"reference_id", candidate.ReferenceID, "generation", candidate.Generation)
		}
	}
}

func (h *DocumentHousekeepingService) cleanupTerminalSpans(ctx context.Context) {
	if !h.spanRetentionEnabled || h.spanRetentionBatch < 1 {
		return
	}
	now := time.Now().UTC()
	result := h.db.WithContext(ctx).Exec(`WITH doomed AS (
		SELECT s.id FROM document_processing_spans s
		WHERE s.finished_at IS NOT NULL
		  AND ((s.status = 'succeeded' AND s.finished_at < ?) OR (s.status IN ('failed','cancelled') AND s.finished_at < ?))
		  AND NOT EXISTS (SELECT 1 FROM document_processing_spans child WHERE child.parent_span_id = s.id)
		ORDER BY s.finished_at ASC, s.id ASC LIMIT ?
	) DELETE FROM document_processing_spans WHERE id IN (SELECT id FROM doomed)`, now.Add(-h.spanSuccessRetention), now.Add(-h.spanFailureRetention), h.spanRetentionBatch)
	if result.Error != nil {
		logger.Warn(ctx, "processing span retention failed", "error", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		logger.Info(ctx, "terminal processing spans retained", "deleted", result.RowsAffected)
	}
}

// FailInterruptedWork applies the restart policy to durable stage work. It
// runs once at process startup, before the Asynq server starts consuming
// tasks, so no recovered worker can observe a half-finished row first.
//
// The process is single-run by design: any row still marked running belongs
// to an execution whose worker died with the previous process. Those rows are
// failed immediately so the user can retry through the normal entry points,
// instead of leaving the stage stuck until recoverStaleStages reaches its
// stale threshold (70 minutes by default). The generation is deliberately
// unchanged; a retry bumps it, which fences any recovered task payload that
// Asynq redelivers later (TryStartStage then revokes it).
//
// Queued rows are left alone: their Asynq tasks survive a restart and are
// redelivered normally. Catalog source/reference work is the exception
// because its in-process dispatcher queue is gone; those rows are failed in
// both queued and running states.
func (h *DocumentHousekeepingService) FailInterruptedWork(ctx context.Context) error {
	if h == nil || h.db == nil {
		return nil
	}
	now := time.Now().UTC()
	message := "server restarted before processing completed"

	// Markdown recognition and metadata extraction own their Asynq tasks, so
	// only running rows are orphaned here.
	if err := h.db.WithContext(ctx).Exec(`UPDATE documents
		SET markdown_status = 'failed',
		    markdown_error_code = ?,
		    markdown_error_message = ?,
		    markdown_updated_at = ?
		WHERE source_state = 'available'
		  AND markdown_status = 'running'`,
		types.ProcessingErrorTaskFailed, message, now).Error; err != nil {
		return err
	}
	if err := h.db.WithContext(ctx).Exec(`UPDATE documents
		SET metadata_extraction_status = 'failed',
		    metadata_extraction_error_code = ?,
		    metadata_extraction_error_message = ?,
		    metadata_extraction_updated_at = ?
		WHERE source_state = 'available'
		  AND metadata_extraction_status = 'running'`,
		types.ProcessingErrorTaskFailed, message, now).Error; err != nil {
		return err
	}

	// Catalog lookups are handed to the in-process dispatcher and outlive the
	// Asynq stage task. Both queued and running rows lose their execution
	// state on restart.
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
	// Unresolved references are dispatched per row; resolved rows keep their
	// result and must not be touched.
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
	// work after restart. Spans that are retried by their Asynq task (for
	// example remote downloads) are deliberately excluded; the same task is
	// redelivered and starts a new span.
	if err := h.db.WithContext(ctx).Exec(`UPDATE document_processing_spans
		SET status = 'cancelled',
		    error_code = ?,
		    error_reason = ?,
		    finished_at = ?,
		    updated_at = ?
		WHERE status IN ('queued','running')
		  AND kind IN (
		      'recognition', 'metadata_extraction',
		      'catalog_source', 'catalog_reference', 'catalog_provider'
		  )`,
		types.ProcessingErrorCancelled, message, now, now).Error; err != nil {
		return err
	}
	logger.Info(ctx, "interrupted processing work marked failed", "reason", "server restart")
	return nil
}

func stageStateColumn(stage types.DocumentStage) string      { return stageColumn(stage, "status") }
func stageGenerationColumn(stage types.DocumentStage) string { return stageColumn(stage, "generation") }
func stageUpdatedAtColumn(stage types.DocumentStage) string  { return stageColumn(stage, "updated_at") }
func stageErrorCodeColumn(stage types.DocumentStage) string  { return stageColumn(stage, "error_code") }
func stageErrorMessageColumn(stage types.DocumentStage) string {
	return stageColumn(stage, "error_message")
}
func stageColumn(stage types.DocumentStage, suffix string) string {
	switch stage {
	case types.DocumentStageMarkdown:
		return "markdown_" + suffix
	case types.DocumentStageMetadataExtraction:
		return "metadata_extraction_" + suffix
	case types.DocumentStageSourceLookup:
		return "source_lookup_" + suffix
	case types.DocumentStageReferenceLookup:
		return "reference_lookup_" + suffix
	default:
		return ""
	}
}
