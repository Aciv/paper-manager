package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const catalogIdlePoll = 100 * time.Millisecond

var errCatalogProviderPanicked = errors.New("catalog provider worker panicked")

// CatalogDispatcher owns the in-memory provider query queue. One worker per
// configured provider takes jobs from the shared priority queue, performs the
// HTTP request, and persists the result through a durable Asynq task. It is a
// single-process component by design: restart marks durable rows failed rather
// than reconstructing this state.
type CatalogDispatcher struct {
	cfg       config.Config
	queue     *catalogQueue
	papers    interfaces.PaperService
	tasks     interfaces.TaskEnqueuer
	spans     interfaces.SpanRecorder
	documents interfaces.DocumentRepository

	providers []interfaces.PaperCatalogProvider

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewCatalogDispatcher creates the in-process dispatcher. It deliberately
// does not depend on PaperInfoService; result post-processing flows through
// the task queue to avoid a dependency cycle.
func NewCatalogDispatcher(
	cfg config.Config,
	catalogs interfaces.PaperCatalogRegistry,
	papers interfaces.PaperService,
	tasks interfaces.TaskEnqueuer,
	spans interfaces.SpanRecorder,
	documents interfaces.DocumentRepository,
) *CatalogDispatcher {
	providers := catalogs.Providers()
	maxTimeout, maxSpacing := time.Duration(0), time.Duration(0)
	for _, provider := range providers {
		if timeout := cfg.PaperCatalogTimeoutFor(provider.Name()); timeout > maxTimeout {
			maxTimeout = timeout
		}
		if spacing := cfg.PaperCatalogSpacingFor(provider.Name()); spacing > maxSpacing {
			maxSpacing = spacing
		}
	}
	jobDeadline := (maxTimeout+maxSpacing)*time.Duration(len(providers)+1) + 2*time.Minute
	return &CatalogDispatcher{
		cfg:       cfg,
		papers:    papers,
		tasks:     tasks,
		spans:     spans,
		documents: documents,
		providers: providers,
		queue: newCatalogQueue(
			len(providers),
			cfg.PaperReferenceCatalogConcurrency,
			cfg.PaperReferenceCatalogPerDocumentConcurrency,
			cfg.PaperCatalogSourceStopScore,
			jobDeadline,
		),
	}
}

// Start launches one provider worker per configured catalog. It is safe to
// call more than once; only the first call starts workers.
func (d *CatalogDispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return
	}
	child, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.started = true
	for _, provider := range d.providers {
		provider := provider
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.runProvider(child, provider)
		}()
	}
}

// Stop cancels in-flight work and rejects new submissions. Durable rows are
// reconciled by the startup failure sweep on the next boot.
func (d *CatalogDispatcher) Stop() {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
	jobs := d.queue.stop()
	if len(jobs) > 0 {
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		for _, job := range jobs {
			d.failJob(ctx, job, "", types.ProcessingErrorTaskFailed,
				"catalog dispatcher stopped before work completed")
		}
		stop()
	}
}

// SubmitSource places one source-paper lookup into the high-priority queue.
func (d *CatalogDispatcher) SubmitSource(ctx context.Context, job types.CatalogSourceJob) error {
	if len(d.providers) == 0 {
		return ErrPaperInfoCatalogUnavailable
	}
	if err := d.queue.submitSource(job); err != nil {
		return err
	}
	logger.Info(ctx, "source catalog job queued",
		"document_id", job.DocumentID, "generation", job.Generation)
	return nil
}

// SubmitReference places one citation lookup into the low-priority queue.
func (d *CatalogDispatcher) SubmitReference(ctx context.Context, job types.CatalogReferenceJob) error {
	if len(d.providers) == 0 {
		return ErrPaperInfoCatalogUnavailable
	}
	if err := d.queue.submitReference(job); err != nil {
		return err
	}
	logger.Info(ctx, "reference catalog job queued",
		"reference_id", job.Reference.ReferenceID, "generation", job.Generation)
	return nil
}

// HasReference reports whether a reference generation is still queued or
// executing in the in-process dispatcher.
func (d *CatalogDispatcher) HasReference(referenceID uuid.UUID, generation int64) bool {
	return d.queue.hasReference(referenceID, generation)
}

func (d *CatalogDispatcher) runProvider(ctx context.Context, provider interfaces.PaperCatalogProvider) {
	name := provider.Name()
	spacing := d.cfg.PaperCatalogSpacingFor(name)
	if spacing <= 0 {
		spacing = 100 * time.Millisecond
	}
	nextAt := time.Now()
	var coolingUntil time.Time

	for {
		wait := nextAt
		if coolingUntil.After(wait) {
			wait = coolingUntil
		}
		if err := waitUntil(ctx, wait); err != nil {
			return
		}
		job, expired := d.queue.take(name)
		if len(expired) > 0 {
			d.enqueueCompletions(ctx, expiredFinal(expired))
		}
		if job == nil {
			if err := waitUntil(ctx, time.Now().Add(catalogIdlePoll)); err != nil {
				return
			}
			continue
		}

		retryAfter := d.executeJob(ctx, provider, name, job)
		nextAt = time.Now().Add(spacing)
		if retryAfter > 0 {
			coolingUntil = time.Now().Add(retryAfter)
		}
	}
}

func (d *CatalogDispatcher) executeJob(
	ctx context.Context, provider interfaces.PaperCatalogProvider, providerName string, job *catalogJob,
) (retryAfter time.Duration) {
	completed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error(ctx, "catalog provider worker panicked",
				"provider", providerName, "document_id", job.documentID,
				"reference_id", job.referenceID, "panic", recovered)
			if !completed {
				// Release the in-flight slot and fail this generation rather than
				// retrying the same panicking provider forever.
				d.enqueueCompletions(ctx, d.queue.abort(job, providerName, false))
			}
			retryAfter = 0
		}
	}()

	if job.kind == types.CatalogJobReference {
		if !job.started {
			reference, applied, err := d.papers.StartReferenceResolution(ctx, job.referenceID, job.generation)
			if err != nil {
				logger.Warn(ctx, "start reference resolution failed",
					"reference_id", job.referenceID, "generation", job.generation, "error", err)
				d.enqueueCompletions(ctx, d.queue.abort(job, providerName, true))
				return 0
			}
			if !applied {
				logger.Info(ctx, "drop stale reference catalog job",
					"reference_id", job.referenceID, "generation", job.generation)
				d.queue.dropJob(job)
				return 0
			}
			job.started = true
			job.reference = reference
		}
		if documentID, found, err := d.papers.FindLocalReferenceMatch(ctx, job.reference, job.citingID); err != nil {
			logger.Warn(ctx, "local reference match failed",
				"reference_id", job.referenceID, "error", err)
			d.enqueueCompletions(ctx, d.queue.abort(job, providerName, true))
			return 0
		} else if found {
			logger.Info(ctx, "reference matched local library before provider lookup",
				"reference_id", job.referenceID, "document_id", documentID)
			d.enqueueCompletions(ctx, d.queue.completeLocalMatch(job, documentID))
			return 0
		}
	}

	spanID, stopSpan := d.beginProviderSpan(ctx, job, providerName)
	defer stopSpan()
	spanFinished := false
	defer func() {
		if !spanFinished {
			d.finishProviderSpan(ctx, spanID, errCatalogProviderPanicked)
		}
	}()
	candidates, err := d.searchProvider(ctx, provider, providerName, job.query)
	d.finishProviderSpan(ctx, spanID, err)
	spanFinished = true

	result := evaluateCatalogCandidates(job.query, candidates, err)
	completions := d.queue.complete(job, providerName, result)
	completed = true
	d.enqueueCompletions(ctx, completions)

	if err != nil {
		logger.Warn(ctx, "catalog provider query failed",
			"provider", providerName, "document_id", job.documentID,
			"reference_id", job.referenceID, "error", err)
		return catalogRetryAfter(err)
	}
	return 0
}

// searchProvider contains a provider panic so it becomes one failed attempt
// instead of leaving the job in-flight or retrying the same provider forever.
func (d *CatalogDispatcher) searchProvider(
	ctx context.Context, provider interfaces.PaperCatalogProvider, providerName string,
	query types.PaperLookupQuery,
) (candidates []types.PaperCatalogCandidate, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error(ctx, "catalog provider panicked during query",
				"provider", providerName, "panic", recovered)
			candidates = nil
			err = errCatalogProviderPanicked
		}
	}()
	queryCtx, cancel := context.WithTimeout(ctx, d.cfg.PaperCatalogTimeoutFor(providerName)+5*time.Second)
	defer cancel()
	return provider.Search(queryCtx, query)
}

func evaluateCatalogCandidates(
	query types.PaperLookupQuery, candidates []types.PaperCatalogCandidate, queryErr error,
) catalogJobResult {
	if queryErr != nil {
		return catalogJobResult{err: queryErr}
	}
	var best *types.PaperCatalogCandidate
	bestScore, exact := 0.0, false
	for index := range candidates {
		candidate := candidates[index]
		if err := candidate.Validate(); err != nil {
			continue
		}
		score, candidateExact := scorePaperCatalogCandidate(query, candidate)
		if best == nil || candidateExact && !exact || !exact && score > bestScore {
			copied := candidate
			best = &copied
			bestScore, exact = score, candidateExact
		}
	}
	if best == nil || (!exact && bestScore < minPaperCatalogMatchScore) {
		return catalogJobResult{noMatch: true}
	}
	if exact {
		bestScore = 1
	}
	return catalogJobResult{candidate: best, score: bestScore, exact: exact}
}

func catalogRetryAfter(err error) time.Duration {
	var retryAfter interface{ TaskRetryAfter() time.Duration }
	if errors.As(err, &retryAfter) {
		return retryAfter.TaskRetryAfter()
	}
	return 0
}

// beginProviderSpan records one standalone provider operation. The dispatcher
// intentionally does not own a cross-task parent span; stage/reference state in
// PostgreSQL remains the source of truth for overall progress.
func (d *CatalogDispatcher) beginProviderSpan(
	ctx context.Context, job *catalogJob, providerName string,
) (uuid.UUID, func()) {
	if d.spans == nil {
		return uuid.Nil, func() {}
	}
	var base types.ProcessingSpanScopeID
	if job.kind == types.CatalogJobReference {
		base = types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogReference, job.referenceID.String())
	} else {
		base = types.ComposeProcessingSpanScopeID(types.ProcessingSpanCatalogSource, job.documentID.String())
	}
	scopeID := types.ComposeProcessingSpanScopeID(
		types.ProcessingSpanCatalogProvider, base.String(),
		types.NormalizePaperCatalogProvider(providerName).String(),
	)
	spanID, err := d.spans.BeginSpan(
		ctx, job.documentID, int(job.generation),
		types.ProcessingSpanCatalogProvider, types.ProcessingSpanScopeProvider, scopeID, 0,
	)
	if err != nil {
		logger.Warn(ctx, "start catalog provider span failed",
			"provider", providerName, "document_id", job.documentID, "error", err)
		return uuid.Nil, func() {}
	}
	stop := keepSpanAlive(ctx, d.spans, spanID)
	return spanID, stop
}

func (d *CatalogDispatcher) finishProviderSpan(ctx context.Context, spanID uuid.UUID, queryErr error) {
	if d.spans == nil || spanID == uuid.Nil {
		return
	}
	if queryErr != nil {
		code, reason := spanFailure(queryErr)
		_ = d.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanFailed, code, reason)
		return
	}
	_ = d.spans.FinishSpan(context.WithoutCancel(ctx), spanID, types.ProcessingSpanSucceeded, "", "")
}

func (d *CatalogDispatcher) enqueueCompletions(ctx context.Context, completions []catalogCompletion) {
	for _, completion := range completions {
		payload := types.PaperQueryApplyPayload{
			RequestID: completion.job.requestID, Kind: completion.job.kind,
			DocumentID: completion.job.documentID.String(),
			Generation: completion.job.generation, Provider: completion.provider,
			Final: completion.final, Outcome: completion.outcome,
			Candidate: completion.candidate, ErrorCode: completion.errorCode,
			ErrorMessage: completion.errorMessage, Query: completion.job.query,
		}
		if completion.job.kind == types.CatalogJobReference {
			payload.DocumentID = completion.job.citingID.String()
			payload.ReferenceID = completion.job.referenceID.String()
			payload.Stage = types.DocumentStageReferenceLookup
		} else {
			payload.Stage = types.DocumentStageSourceLookup
		}
		if completion.resolvedDocID != uuid.Nil {
			payload.ResolvedDocumentID = completion.resolvedDocID.String()
		}
		task, options, err := NewPaperQueryApplyTask(
			payload, d.cfg.PaperCatalogTaskTimeout, d.cfg.PaperCatalogTaskMaxRetry,
		)
		if err == nil {
			err = d.enqueueResultTask(ctx, task, options, payload.DocumentID)
		}
		if err == nil {
			continue
		}
		logger.Error(ctx, "enqueue catalog result task failed",
			"document_id", payload.DocumentID, "reference_id", payload.ReferenceID,
			"kind", completion.job.kind, "final", completion.final, "error", err)
		if completion.final {
			// The in-memory job has already been removed. Persist the terminal
			// state now rather than leaving the stage/reference running until
			// housekeeping or restart recovery.
			d.failJob(ctx, completion.job, completion.provider,
				types.ProcessingErrorTaskFailed,
				"catalog result task could not be enqueued")
		}
	}
}

func (d *CatalogDispatcher) enqueueResultTask(
	ctx context.Context, task *asynq.Task, options []asynq.Option, documentID string,
) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err = enqueueDocumentStageTask(enqueueCtx, d.tasks, task, options, documentID)
		cancel()
		if err == nil {
			return nil
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return err
			case <-timer.C:
			}
		}
	}
	return err
}

func (d *CatalogDispatcher) failJob(
	ctx context.Context, job *catalogJob, provider, code, message string,
) {
	if job == nil {
		return
	}
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	switch job.kind {
	case types.CatalogJobSource:
		if d.documents == nil {
			return
		}
		_, _, err = d.documents.FinishStage(
			failCtx, job.documentID, types.DocumentStageSourceLookup,
			job.generation, types.DocumentStageFailed, code, message,
		)
	case types.CatalogJobReference:
		if d.papers == nil {
			return
		}
		// A job drained before its first provider attempt is still queued in
		// PostgreSQL. FinishResolution requires running, so begin that attempt
		// first; a no-op here is fine if it is already terminal.
		_, _, startErr := d.papers.StartReferenceResolution(failCtx, job.referenceID, job.generation)
		if startErr != nil {
			logger.Warn(ctx, "start drained reference before failing it failed",
				"reference_id", job.referenceID, "generation", job.generation, "error", startErr)
			return
		}
		_, _, err = d.papers.FinishReferenceResolution(
			failCtx, job.referenceID, job.generation,
			types.DocumentStageFailed, provider, code, message, nil, nil,
		)
	}
	if err != nil {
		logger.Error(ctx, "persist catalog failure after enqueue/dispatch stop failed",
			"document_id", job.documentID, "reference_id", job.referenceID,
			"generation", job.generation, "error", err)
	}
}

func waitUntil(ctx context.Context, at time.Time) error {
	delay := time.Until(at)
	if delay <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ interfaces.CatalogDispatcher = (*CatalogDispatcher)(nil)

// String is useful in focused logs without importing fmt at call sites.
func (j catalogJob) String() string {
	return fmt.Sprintf("%s:%s:%d", j.kind, j.documentID, j.generation)
}
