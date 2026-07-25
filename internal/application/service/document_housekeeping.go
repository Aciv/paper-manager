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

	"gorm.io/gorm"
)

// DocumentHousekeepingService recovers document rows left queued or processing
// after their asynchronous work disappeared. It deliberately checks the task
// backend before marking a stale row failed so normal queue backpressure is
// never mistaken for an orphaned parse.
type DocumentHousekeepingService struct {
	db             *gorm.DB
	inspector      interfaces.TaskInspector
	enabled        bool
	interval       time.Duration
	staleThreshold time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewDocumentHousekeepingService configures the periodic orphan recovery
// worker. Start is separate so dependency construction has no background side
// effects and the server owns its lifecycle.
func NewDocumentHousekeepingService(
	db *gorm.DB, cfg config.Config, inspector interfaces.TaskInspector,
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
		db: db, inspector: inspector,
		enabled:  cfg.DocumentHousekeepingEnabled,
		interval: interval, staleThreshold: staleThreshold,
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

func (h *DocumentHousekeepingService) runSweep(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cutoff := time.Now().UTC().Add(-h.staleThreshold)

	var candidates []types.Document
	if err := h.db.WithContext(sweepCtx).
		Where("status IN ? AND updated_at < ?", []types.DocumentStatus{
			types.DocumentStatusQueued, types.DocumentStatusProcessing,
		}, cutoff).
		Find(&candidates).Error; err != nil {
		logger.Warn(ctx, "document housekeeping candidate query failed", "error", err)
		return
	}

	stuck, queueSkipped := h.filterOutQueued(sweepCtx, candidates)
	if len(stuck) == 0 {
		if queueSkipped > 0 {
			logger.Info(ctx, "document housekeeping skipped queued documents", "count", queueSkipped)
		}
		return
	}

	ids := make([]string, 0, len(stuck))
	for _, document := range stuck {
		ids = append(ids, document.ID.String())
	}
	message := "document task became stale without queued work; recovered by housekeeping"
	result := h.db.WithContext(sweepCtx).Model(&types.Document{}).
		Where("id IN ? AND status IN ? AND updated_at < ?", ids, []types.DocumentStatus{
			types.DocumentStatusQueued, types.DocumentStatusProcessing,
		}, cutoff).
		Updates(map[string]interface{}{
			"status":        types.DocumentStatusFailed,
			"error_message": message,
		})
	if result.Error != nil {
		logger.Warn(ctx, "document housekeeping update failed", "error", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		logger.Info(ctx, "document housekeeping recovered stale documents",
			"recovered", result.RowsAffected, "stale_threshold", h.staleThreshold.String())
	}
	if queueSkipped > 0 {
		logger.Info(ctx, "document housekeeping skipped queued documents", "count", queueSkipped)
	}
}

// filterOutQueued removes candidates that still have pending, scheduled,
// retrying, or active work. An inspector error keeps the row as a candidate:
// this matches the existing stale-row recovery fail-safe and makes backend
// visibility failures observable through a later conditional update.
func (h *DocumentHousekeepingService) filterOutQueued(
	ctx context.Context, candidates []types.Document,
) (kept []types.Document, skipped int) {
	if h.inspector == nil || len(candidates) == 0 {
		return candidates, 0
	}
	kept = candidates[:0]
	for _, document := range candidates {
		queued, err := h.inspector.HasQueuedTasksForDocument(ctx, document.ID.String())
		if err != nil {
			logger.Warn(ctx, "document housekeeping queue probe failed; treating as stale",
				"document_id", document.ID, "error", err)
			kept = append(kept, document)
			continue
		}
		if queued {
			skipped++
			continue
		}
		kept = append(kept, document)
	}
	return kept, skipped
}
