package service

import (
	"errors"
	"sync"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// errCatalogDispatcherStopped tells the dispatch task that the in-process
// provider queue no longer accepts work.
var errCatalogDispatcherStopped = errors.New("catalog dispatcher is stopped")

type catalogJobKey struct {
	kind types.CatalogJobKind
	id   uuid.UUID
}

// catalogJob is one provider round. It lives entirely in process memory; a
// restart intentionally marks the durable stage/reference rows failed rather
// than trying to reconstruct this state.
type catalogJob struct {
	kind        types.CatalogJobKind
	documentID  uuid.UUID
	referenceID uuid.UUID
	citingID    uuid.UUID
	requestID   string
	generation  int64
	query       types.PaperLookupQuery
	reference   types.PaperReference
	deadline    time.Time

	tried      map[string]bool
	sawNoMatch bool
	lastErr    error
	best       *types.PaperCatalogCandidate
	bestScore  float64
	started    bool
	stale      bool
}

func (j *catalogJob) key() catalogJobKey {
	if j.kind == types.CatalogJobReference {
		return catalogJobKey{kind: j.kind, id: j.referenceID}
	}
	return catalogJobKey{kind: j.kind, id: j.documentID}
}

type catalogJobResult struct {
	candidate *types.PaperCatalogCandidate
	score     float64
	exact     bool
	noMatch   bool
	err       error
}

// catalogCompletion is one post-processing action produced by the queue. The
// provider worker turns it into an Asynq task after releasing the queue lock.
type catalogCompletion struct {
	job           *catalogJob
	provider      string
	outcome       types.CatalogOutcome
	final         bool
	candidate     *types.PaperCatalogCandidate
	resolvedDocID uuid.UUID
	errorCode     string
	errorMessage  string
}

// catalogQueue is the shared priority queue. Paper jobs are always scanned
// before reference jobs. A job is removed while a provider executes it, so a
// second provider can never hold the same row concurrently.
type catalogQueue struct {
	mu sync.Mutex

	paper []*catalogJob
	refs  []*catalogJob
	index map[catalogJobKey]*catalogJob

	refsInFlight    int
	refsPerDocument map[uuid.UUID]int

	maxRefs            int
	maxRefsPerDocument int
	providerCount      int
	stopScore          float64
	jobDeadline        time.Duration
	stopped            bool
}

func newCatalogQueue(providerCount, maxRefs, maxRefsPerDocument int, stopScore float64, jobDeadline time.Duration) *catalogQueue {
	if providerCount < 1 {
		providerCount = 1
	}
	if maxRefs < 1 {
		maxRefs = 1
	}
	if maxRefsPerDocument < 1 {
		maxRefsPerDocument = 1
	}
	if stopScore <= 0 || stopScore > 1 {
		stopScore = 1
	}
	if jobDeadline <= 0 {
		jobDeadline = 30 * time.Minute
	}
	return &catalogQueue{
		index:              make(map[catalogJobKey]*catalogJob),
		refsPerDocument:    make(map[uuid.UUID]int),
		maxRefs:            maxRefs,
		maxRefsPerDocument: maxRefsPerDocument,
		providerCount:      providerCount,
		stopScore:          stopScore,
		jobDeadline:        jobDeadline,
	}
}

func (q *catalogQueue) submitSource(job types.CatalogSourceJob) error {
	if job.DocumentID == uuid.Nil || job.Generation < 1 {
		return errors.New("source catalog job requires document and generation")
	}
	if err := job.Query.Validate(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return errCatalogDispatcherStopped
	}
	next := &catalogJob{
		kind: types.CatalogJobSource, documentID: job.DocumentID,
		requestID: job.RequestID, generation: job.Generation, query: job.Query,
		deadline: time.Now().Add(q.jobDeadline), tried: make(map[string]bool),
	}
	q.replaceLocked(next, &q.paper)
	return nil
}

func (q *catalogQueue) submitReference(job types.CatalogReferenceJob) error {
	reference := job.Reference
	if reference.ReferenceID == uuid.Nil || reference.CitingDocumentID == uuid.Nil || job.Generation < 1 {
		return errors.New("reference catalog job requires reference, citing document and generation")
	}
	if err := job.Query.Validate(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return errCatalogDispatcherStopped
	}
	next := &catalogJob{
		kind: types.CatalogJobReference, documentID: reference.CitingDocumentID,
		citingID: reference.CitingDocumentID, referenceID: reference.ReferenceID,
		requestID: job.RequestID, generation: job.Generation, query: job.Query,
		reference: reference, deadline: time.Now().Add(q.jobDeadline),
		tried: make(map[string]bool),
	}
	q.replaceLocked(next, &q.refs)
	return nil
}

// replaceLocked installs a newer generation and invalidates any queued or
// in-flight job with the same identity. The in-flight worker observes stale
// and discards its result; its counter is released in complete/abort.
func (q *catalogQueue) replaceLocked(next *catalogJob, list *[]*catalogJob) {
	key := next.key()
	if existing, ok := q.index[key]; ok {
		if existing.generation >= next.generation {
			return
		}
		existing.stale = true
		q.removeFromSliceLocked(existing, &q.paper)
		q.removeFromSliceLocked(existing, &q.refs)
	}
	q.index[key] = next
	*list = append(*list, next)
}

// take scans source jobs first, then reference jobs subject to the global and
// per-citing-document admission caps. Expired queued jobs are returned so the
// caller can emit their terminal post-processing task outside the lock.
func (q *catalogQueue) take(provider string) (*catalogJob, []*catalogJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return nil, nil
	}
	now := time.Now()
	expired := make([]*catalogJob, 0)

	job := q.takeFromLocked(&q.paper, provider, now, &expired, nil)
	if job != nil {
		return job, expired
	}
	if q.refsInFlight >= q.maxRefs {
		return nil, expired
	}
	job = q.takeFromLocked(&q.refs, provider, now, &expired, func(j *catalogJob) bool {
		return q.refsPerDocument[j.citingID] < q.maxRefsPerDocument
	})
	if job != nil {
		q.refsInFlight++
		q.refsPerDocument[job.citingID]++
	}
	return job, expired
}

func (q *catalogQueue) takeFromLocked(
	list *[]*catalogJob, provider string, now time.Time, expired *[]*catalogJob, admit func(*catalogJob) bool,
) *catalogJob {
	for index := 0; index < len(*list); {
		job := (*list)[index]
		if job.deadline.Before(now) {
			*list = append((*list)[:index], (*list)[index+1:]...)
			delete(q.index, job.key())
			*expired = append(*expired, job)
			continue
		}
		if job.tried[provider] || (admit != nil && !admit(job)) {
			index++
			continue
		}
		*list = append((*list)[:index], (*list)[index+1:]...)
		job.tried[provider] = true
		return job
	}
	return nil
}

// complete records one provider outcome and returns the post-processing
// actions the worker must enqueue. It never performs I/O while locked.
func (q *catalogQueue) complete(job *catalogJob, provider string, result catalogJobResult) []catalogCompletion {
	q.mu.Lock()
	defer q.mu.Unlock()

	if job.kind == types.CatalogJobReference {
		q.releaseReferenceLocked(job)
	}
	if job.stale || q.index[job.key()] != job {
		return nil
	}

	switch {
	case result.err != nil:
		job.lastErr = result.err
	case result.candidate != nil:
		job.lastErr = nil
		if result.score >= minPaperCatalogMatchScore && (job.best == nil || result.score > job.bestScore) {
			candidate := *result.candidate
			job.best = &candidate
			job.bestScore = result.score
		}
	default:
		job.sawNoMatch = true
	}

	if job.kind == types.CatalogJobSource {
		return q.completeSourceLocked(job, provider, result)
	}
	return q.completeReferenceLocked(job, provider, result)
}

func (q *catalogQueue) completeSourceLocked(job *catalogJob, provider string, result catalogJobResult) []catalogCompletion {
	allTried := len(job.tried) >= q.providerCount
	if result.candidate != nil && result.score >= minPaperCatalogMatchScore {
		if allTried {
			return q.finalLocked(job, provider, types.CatalogOutcomeMatched, job.best)
		}
		if result.exact || result.score >= q.stopScore {
			return q.finalLocked(job, provider, types.CatalogOutcomeMatched, result.candidate)
		}
		// Persist this candidate immediately, then let another provider try to
		// improve it. job.best remains the highest-scoring candidate seen so far
		// and is used if the round ends without an exact/stop hit.
		q.paper = append(q.paper, job)
		return []catalogCompletion{{
			job: job, provider: provider, candidate: result.candidate,
		}}
	}
	if allTried {
		if job.best != nil {
			return q.finalLocked(job, provider, types.CatalogOutcomeMatched, job.best)
		}
		if job.sawNoMatch {
			return q.finalLocked(job, provider, types.CatalogOutcomeNoMatch, nil)
		}
		return q.finalLocked(job, provider, types.CatalogOutcomeFailed, nil)
	}
	q.paper = append(q.paper, job)
	return nil
}

func (q *catalogQueue) completeReferenceLocked(job *catalogJob, provider string, result catalogJobResult) []catalogCompletion {
	allTried := len(job.tried) >= q.providerCount
	if result.candidate != nil && result.score >= minPaperCatalogMatchScore {
		return q.finalLocked(job, provider, types.CatalogOutcomeMatched, result.candidate)
	}
	if allTried {
		if job.best != nil {
			return q.finalLocked(job, provider, types.CatalogOutcomeMatched, job.best)
		}
		if job.sawNoMatch {
			return q.finalLocked(job, provider, types.CatalogOutcomeNoMatch, nil)
		}
		return q.finalLocked(job, provider, types.CatalogOutcomeFailed, nil)
	}
	q.refs = append(q.refs, job)
	return nil
}

func (q *catalogQueue) finalLocked(job *catalogJob, provider string, outcome types.CatalogOutcome, candidate *types.PaperCatalogCandidate) []catalogCompletion {
	delete(q.index, job.key())
	completion := catalogCompletion{
		job: job, provider: provider, outcome: outcome, final: true, candidate: candidate,
	}
	if outcome == types.CatalogOutcomeFailed {
		completion.errorCode = types.ProcessingErrorServiceUnavailable
		completion.errorMessage = "paper catalog providers were unavailable"
	}
	return []catalogCompletion{completion}
}

// completeLocalMatch short-circuits a reference job with a local-library hit.
func (q *catalogQueue) completeLocalMatch(job *catalogJob, documentID uuid.UUID) []catalogCompletion {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job.kind == types.CatalogJobReference {
		q.releaseReferenceLocked(job)
	}
	if job.stale || q.index[job.key()] != job {
		return nil
	}
	delete(q.index, job.key())
	return []catalogCompletion{{
		job: job, outcome: types.CatalogOutcomeMatched, final: true,
		resolvedDocID: documentID,
	}}
}

// abort releases a provider's in-flight slot after a panic or a failed
// reference start transition. When retry is false the job is finalized as
// failed; when true it is made runnable by another provider.
func (q *catalogQueue) abort(job *catalogJob, provider string, retry bool) []catalogCompletion {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job.kind == types.CatalogJobReference {
		q.releaseReferenceLocked(job)
	}
	if job.stale || q.index[job.key()] != job {
		return nil
	}
	if retry && time.Now().Before(job.deadline) {
		delete(job.tried, provider)
		if job.kind == types.CatalogJobSource {
			q.paper = append(q.paper, job)
		} else {
			q.refs = append(q.refs, job)
		}
		return nil
	}
	return q.finalLocked(job, provider, types.CatalogOutcomeFailed, nil)
}

// dropJob removes an in-flight job without emitting a terminal action. It is
// used when the durable generation/status check proves the work is stale.
func (q *catalogQueue) dropJob(job *catalogJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job.kind == types.CatalogJobReference {
		q.releaseReferenceLocked(job)
	}
	if q.index[job.key()] == job {
		delete(q.index, job.key())
	}
}

func (q *catalogQueue) releaseReferenceLocked(job *catalogJob) {
	if q.refsInFlight > 0 {
		q.refsInFlight--
	}
	if count := q.refsPerDocument[job.citingID]; count > 0 {
		if count == 1 {
			delete(q.refsPerDocument, job.citingID)
		} else {
			q.refsPerDocument[job.citingID] = count - 1
		}
	}
}

// expiredFinal completions turn queued jobs past their deadline into failed
// post-processing tasks.
func expiredFinal(jobs []*catalogJob) []catalogCompletion {
	completions := make([]catalogCompletion, 0, len(jobs))
	for _, job := range jobs {
		completions = append(completions, catalogCompletion{
			job: job, outcome: types.CatalogOutcomeFailed, final: true,
			errorCode:    types.ProcessingErrorServiceUnavailable,
			errorMessage: "paper catalog lookup exceeded its deadline",
		})
	}
	return completions
}

// hasReference reports whether the exact reference generation is still queued
// or executing in this process. Housekeeping uses it instead of scanning Asynq,
// which no longer carries per-reference query tasks.
func (q *catalogQueue) hasReference(referenceID uuid.UUID, generation int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.index[catalogJobKey{kind: types.CatalogJobReference, id: referenceID}]
	return ok && job.generation == generation && !job.stale
}

// stop closes the queue and returns every queued or in-flight job so the
// dispatcher can persist a terminal failure during graceful shutdown.
func (q *catalogQueue) stop() []*catalogJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	seen := make(map[*catalogJob]struct{})
	jobs := make([]*catalogJob, 0, len(q.paper)+len(q.refs))
	add := func(job *catalogJob) {
		if job == nil {
			return
		}
		if _, exists := seen[job]; exists {
			return
		}
		seen[job] = struct{}{}
		jobs = append(jobs, job)
	}
	for _, job := range q.paper {
		add(job)
	}
	for _, job := range q.refs {
		add(job)
	}
	for _, job := range q.index {
		add(job)
	}
	q.paper = nil
	q.refs = nil
	q.index = make(map[catalogJobKey]*catalogJob)
	return jobs
}

func (q *catalogQueue) removeFromSliceLocked(job *catalogJob, list *[]*catalogJob) {
	for index, candidate := range *list {
		if candidate == job {
			*list = append((*list)[:index], (*list)[index+1:]...)
			return
		}
	}
}
