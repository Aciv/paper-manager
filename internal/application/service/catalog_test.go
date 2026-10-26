package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"net/http"
	"os"
	"paperMaster/internal/config"
	"paperMaster/internal/infrastructure/papercatalog"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from catalog_dispatcher_test.go.
// ---------------------------------------------------------------------------

type catalogTaskCapture struct {
	mu    sync.Mutex
	tasks []*asynq.Task
	apply func(types.PaperQueryApplyPayload) error
}

func (q *catalogTaskCapture) EnqueueContext(
	_ context.Context, task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	q.mu.Lock()
	q.tasks = append(q.tasks, task)
	q.mu.Unlock()
	if q.apply != nil && task.Type() == types.TypePaperQueryApply {
		var payload types.PaperQueryApplyPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return nil, err
		}
		if err := q.apply(payload); err != nil {
			return nil, err
		}
	}
	return &asynq.TaskInfo{ID: "captured"}, nil
}

func (q *catalogTaskCapture) payloads() []types.PaperQueryApplyPayload {
	q.mu.Lock()
	defer q.mu.Unlock()
	payloads := make([]types.PaperQueryApplyPayload, 0, len(q.tasks))
	for _, task := range q.tasks {
		if task.Type() != types.TypePaperQueryApply {
			continue
		}
		var payload types.PaperQueryApplyPayload
		if err := json.Unmarshal(task.Payload(), &payload); err == nil {
			payloads = append(payloads, payload)
		}
	}
	return payloads
}

func (q *catalogTaskCapture) snapshot() []*asynq.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]*asynq.Task(nil), q.tasks...)
}

func testCatalogDispatcherConfig(providers ...string) config.Config {
	spacings := make(map[string]time.Duration, len(providers))
	timeouts := make(map[string]time.Duration, len(providers))
	concurrency := make(map[string]int, len(providers))
	for _, provider := range providers {
		spacings[provider] = time.Millisecond
		timeouts[provider] = time.Second
		concurrency[provider] = 1
	}
	return config.Config{
		PaperCatalogProviders:                       providers,
		PaperCatalogTimeout:                         time.Second,
		PaperCatalogConcurrency:                     4,
		PaperCatalogProviderSpacings:                spacings,
		PaperCatalogProviderTimeouts:                timeouts,
		PaperCatalogProviderConcurrency:             concurrency,
		PaperReferenceCatalogConcurrency:            2,
		PaperReferenceCatalogPerDocumentConcurrency: 1,
		PaperCatalogTaskTimeout:                     5 * time.Second,
		PaperCatalogTaskMaxRetry:                    1,
	}
}

// TestCatalogQueuePrioritizesSourceOverReferences verifies that a source job
// submitted after a backlog of references is still dequeued first.
func TestCatalogQueuePrioritizesSourceOverReferences(t *testing.T) {
	queue := newCatalogQueue(4, 100, 100, 0.9, time.Minute)
	for index := 0; index < 20; index++ {
		documentID := uuid.New()
		if err := queue.submitReference(types.CatalogReferenceJob{
			Reference: types.PaperReference{
				ReferenceID: uuid.New(), CitingDocumentID: documentID,
				RawReference: "reference", Title: "Reference",
			},
			Generation: 1,
			Query:      types.PaperLookupQuery{Title: "Reference"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	sourceID := uuid.New()
	if err := queue.submitSource(types.CatalogSourceJob{
		DocumentID: sourceID, Generation: 1,
		Query: types.PaperLookupQuery{Title: "Source"},
	}); err != nil {
		t.Fatal(err)
	}

	job, expired := queue.take("arxiv")
	if len(expired) != 0 {
		t.Fatalf("expired jobs = %d", len(expired))
	}
	if job == nil || job.kind != types.CatalogJobSource || job.documentID != sourceID {
		t.Fatalf("first taken job = %#v, want source %s", job, sourceID)
	}
}

// TestCatalogQueueReferenceAdmissionCaps verifies that per-document and global
// caps bound admitted reference jobs while still allowing another document's job.
func TestCatalogQueueReferenceAdmissionCaps(t *testing.T) {
	queue := newCatalogQueue(4, 2, 1, 0.9, time.Minute)
	citingID := uuid.New()
	for index := 0; index < 5; index++ {
		if err := queue.submitReference(types.CatalogReferenceJob{
			Reference: types.PaperReference{
				ReferenceID: uuid.New(), CitingDocumentID: citingID,
				RawReference: "reference", Title: "Reference",
			},
			Generation: 1,
			Query:      types.PaperLookupQuery{Title: "Reference"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := queue.take("arxiv")
	if first == nil {
		t.Fatal("first reference was not admitted")
	}
	second, _ := queue.take("openalex")
	if second != nil {
		t.Fatalf("per-document cap admitted a second job: %#v", second)
	}

	otherCiting := uuid.New()
	if err := queue.submitReference(types.CatalogReferenceJob{
		Reference: types.PaperReference{
			ReferenceID: uuid.New(), CitingDocumentID: otherCiting,
			RawReference: "other", Title: "Other",
		},
		Generation: 1,
		Query:      types.PaperLookupQuery{Title: "Other"},
	}); err != nil {
		t.Fatal(err)
	}
	third, _ := queue.take("openalex")
	if third == nil {
		t.Fatal("global cap should admit a second job from another document")
	}
	fourth, _ := queue.take("crossref")
	if fourth != nil {
		t.Fatalf("global cap admitted a third job: %#v", fourth)
	}
}

// TestCatalogDispatcherSourceRoundTrip verifies that a submitted source job is
// resolved by the provider and applied, updating metadata while keeping the DOI.
func TestCatalogDispatcherSourceRoundTrip(t *testing.T) {
	documentID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/source",
	})
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID, Title: "Source Paper",
	}
	if _, _, err := documents.QueueStageNewGeneration(context.Background(), documentID, types.DocumentStageSourceLookup); err != nil {
		t.Fatal(err)
	}
	providers := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.1000/source", "Source Paper", 1),
			}, nil
		}},
	}}
	capture := &catalogTaskCapture{}
	dispatcher := NewCatalogDispatcher(testCatalogDispatcherConfig("crossref"), providers, papers, capture, nil, nil)
	service := newPaperInfoService(
		testCatalogDispatcherConfig("crossref"), documents, &paperInfoStorageStub{},
		&paperInfoExtractorStub{}, papers, capture, nil, dispatcher, nil,
	).(*paperInfoService)
	done := make(chan struct{})
	var doneOnce sync.Once
	capture.apply = func(payload types.PaperQueryApplyPayload) error {
		command, err := payload.Command()
		if err != nil {
			return err
		}
		if err := service.QueryApply(context.Background(), command); err != nil {
			return err
		}
		if payload.Final {
			doneOnce.Do(func() { close(done) })
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	if err := service.PaperSourceQueryEnqueue(context.Background(), types.PaperSourceQueryEnqueueCommand{
		DocumentID: documentID, Stage: types.DocumentStageSourceLookup, Generation: 1,
	}); err != nil {
		t.Fatalf("PaperSourceQueryEnqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("source catalog result was not finalized")
	}
	if len(papers.updates) != 1 {
		t.Fatalf("metadata updates = %d, want 1", len(papers.updates))
	}
	if document := documents.documents[documentID]; document.DOI != "10.1000/source" {
		t.Fatalf("document DOI = %q", document.DOI)
	}
}

// TestCatalogDispatcherReferenceRoundTrip verifies that a queued reference is
// resolved through the provider and records the resolving provider on the row.
func TestCatalogDispatcherReferenceRoundTrip(t *testing.T) {
	citingID, referenceID := uuid.New(), uuid.New()
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: citingID,
		RawReference: "A Reliable Paper. Ada. 2024.", Title: "A Reliable Paper",
		Authors:          []types.PaperAuthor{{Name: "Ada Lovelace"}},
		ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
		MetadataSource: types.PaperMetadataSourceExtraction,
	}
	providers := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			candidate := testCatalogCandidate("crossref", "10.1000/reference", "A Reliable Paper", 1)
			return []types.PaperCatalogCandidate{candidate}, nil
		}},
	}}
	capture := &catalogTaskCapture{}
	dispatcher := NewCatalogDispatcher(testCatalogDispatcherConfig("crossref"), providers, papers, capture, nil, nil)
	service := newPaperInfoService(
		testCatalogDispatcherConfig("crossref"), newPaperDocumentRepoStub(), nil, nil,
		papers, capture, nil, dispatcher, nil,
	).(*paperInfoService)
	capture.apply = func(payload types.PaperQueryApplyPayload) error {
		command, err := payload.Command()
		if err != nil {
			return err
		}
		return service.QueryApply(context.Background(), command)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	reference := papers.references[referenceID]
	if err := dispatcher.SubmitReference(context.Background(), types.CatalogReferenceJob{
		Reference: reference, Generation: 1,
		Query: types.PaperLookupQuery{
			Title: "A Reliable Paper", Authors: []string{"Ada Lovelace"},
			Limit: types.DefaultPaperCatalogLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	waitForReference(t, papers, referenceID, types.DocumentStageSucceeded)
	reference = papers.references[referenceID]
	if reference.ResolutionProvider != "crossref" {
		t.Fatalf("resolution provider = %q", reference.ResolutionProvider)
	}
}

// TestCatalogDispatcherStress enqueues many references and verifies the
// reference admission cap, provider serialization, and finalization count.
func TestCatalogDispatcherStress(t *testing.T) {
	referenceCount := 500
	if value := os.Getenv("CATALOG_STRESS_REFERENCES"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			t.Fatalf("invalid CATALOG_STRESS_REFERENCES=%q", value)
		}
		referenceCount = parsed
	}
	const providerCount = 4
	providers := make([]interfaces.PaperCatalogProvider, 0, providerCount)
	var inFlight, maxInFlight, calls atomic.Int64
	for index := 0; index < providerCount; index++ {
		name := []string{"arxiv", "openalex", "semantic_scholar", "crossref"}[index]
		providers = append(providers, &paperInfoProviderStub{
			name: name,
			search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
				current := inFlight.Add(1)
				for {
					previous := maxInFlight.Load()
					if current <= previous || maxInFlight.CompareAndSwap(previous, current) {
						break
					}
				}
				calls.Add(1)
				time.Sleep(time.Millisecond)
				inFlight.Add(-1)
				return nil, nil
			},
		})
	}
	papers := newPaperInfoPaperServiceStub()
	for index := 0; index < referenceCount; index++ {
		referenceID := uuid.New()
		papers.references[referenceID] = types.PaperReference{
			ReferenceID: referenceID, CitingDocumentID: uuid.New(),
			RawReference: "Stress reference", Title: "Stress reference",
			ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
			MetadataSource: types.PaperMetadataSourceExtraction,
		}
	}
	registry := &paperInfoRegistryStub{providers: providers}
	capture := &catalogTaskCapture{}
	cfg := testCatalogDispatcherConfig("arxiv", "openalex", "semantic_scholar", "crossref")
	dispatcher := NewCatalogDispatcher(cfg, registry, papers, capture, nil, nil)
	references := make([]types.PaperReference, 0, len(papers.references))
	for _, reference := range papers.references {
		references = append(references, reference)
	}
	for _, reference := range references {
		if err := dispatcher.SubmitReference(context.Background(), types.CatalogReferenceJob{
			Reference: reference, Generation: 1,
			Query: types.PaperLookupQuery{
				Title: "Stress reference", Limit: types.DefaultPaperCatalogLimit,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	started := time.Now()

	deadline := time.Now().Add(time.Duration(referenceCount)*8*time.Millisecond + 10*time.Second)
	for {
		final := 0
		for _, task := range capture.snapshot() {
			if task.Type() != types.TypePaperQueryApply {
				continue
			}
			var payload types.PaperQueryApplyPayload
			if err := json.Unmarshal(task.Payload(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Final {
				final++
			}
		}
		if final >= referenceCount {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("finalized %d/%d references before deadline", final, referenceCount)
		}
		time.Sleep(10 * time.Millisecond)
	}
	elapsed := time.Since(started)
	if maximum := maxInFlight.Load(); maximum > 2 {
		t.Fatalf("max concurrent provider calls = %d, want <=2 (reference admission cap)", maximum)
	}
	if maximum := maxInFlight.Load(); maximum < 2 {
		t.Fatalf("max concurrent provider calls = %d, want the dispatcher to use both allowed slots", maximum)
	}
	if got := calls.Load(); got != int64(referenceCount*providerCount) {
		t.Fatalf("provider calls = %d, want %d", got, referenceCount*providerCount)
	}
	t.Logf("processed %d references with %d providers in %s", referenceCount, providerCount, elapsed)
	if elapsed > time.Duration(referenceCount)*8*time.Millisecond+10*time.Second {
		t.Fatalf("stress run took %s", elapsed)
	}
}

// TestCatalogDispatcherSourcePriorityUnderLoad verifies that a source job is
// selected by an idle provider while a large reference backlog is running.
func TestCatalogDispatcherSourcePriorityUnderLoad(t *testing.T) {
	var calls atomic.Int64
	sourceSeen := make(chan int64, 1)
	providers := make([]interfaces.PaperCatalogProvider, 0, 4)
	for _, name := range []string{"arxiv", "openalex", "semantic_scholar", "crossref"} {
		name := name
		providers = append(providers, &paperInfoProviderStub{
			name: name,
			search: func(query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
				index := calls.Add(1)
				if query.Title == "Priority Source" {
					select {
					case sourceSeen <- index:
					default:
					}
				}
				time.Sleep(time.Millisecond)
				return nil, nil
			},
		})
	}
	papers := newPaperInfoPaperServiceStub()
	references := make([]types.PaperReference, 0, 200)
	for index := 0; index < 200; index++ {
		referenceID := uuid.New()
		reference := types.PaperReference{
			ReferenceID: referenceID, CitingDocumentID: uuid.New(),
			RawReference: "Backlog reference", Title: "Backlog reference",
			ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
			MetadataSource: types.PaperMetadataSourceExtraction,
		}
		papers.references[referenceID] = reference
		references = append(references, reference)
	}
	registry := &paperInfoRegistryStub{providers: providers}
	capture := &catalogTaskCapture{}
	cfg := testCatalogDispatcherConfig("arxiv", "openalex", "semantic_scholar", "crossref")
	dispatcher := NewCatalogDispatcher(cfg, registry, papers, capture, nil, nil)
	for _, reference := range references {
		if err := dispatcher.SubmitReference(context.Background(), types.CatalogReferenceJob{
			Reference: reference, Generation: 1,
			Query: types.PaperLookupQuery{Title: "Backlog reference", Limit: types.DefaultPaperCatalogLimit},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dispatcher.SubmitSource(context.Background(), types.CatalogSourceJob{
		DocumentID: uuid.New(), Generation: 1,
		Query: types.PaperLookupQuery{Title: "Priority Source", Limit: types.DefaultPaperCatalogLimit},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	select {
	case index := <-sourceSeen:
		if index > 20 {
			t.Fatalf("source query observed after %d provider calls, want priority dispatch", index)
		}
		t.Logf("source query observed after %d provider calls", index)
	case <-time.After(5 * time.Second):
		t.Fatal("source job was not dispatched under reference backlog")
	}
}

// TestCatalogDispatcherRecordsProviderSpanScopes verifies that source and
// reference provider calls open provider-scope spans bound to their owner IDs.
func TestCatalogDispatcherRecordsProviderSpanScopes(t *testing.T) {
	sourceID, citingID, referenceID := uuid.New(), uuid.New(), uuid.New()
	spans := &paperInfoSpanStub{}
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: citingID,
		RawReference: "Reference", Title: "Reference",
		ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
		MetadataSource: types.PaperMetadataSourceExtraction,
	}
	providers := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		&paperInfoProviderStub{name: "crossref", search: func(query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			if query.Title == "Source Paper" {
				return []types.PaperCatalogCandidate{
					testCatalogCandidate("crossref", "10.1000/source", "Source Paper", 1),
				}, nil
			}
			return nil, nil
		}},
	}}
	capture := &catalogTaskCapture{}
	dispatcher := NewCatalogDispatcher(
		testCatalogDispatcherConfig("crossref"), providers, papers, capture, spans, nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	if err := dispatcher.SubmitSource(context.Background(), types.CatalogSourceJob{
		DocumentID: sourceID, Generation: 1,
		Query: types.PaperLookupQuery{DOI: "10.1000/source", Title: "Source Paper"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.SubmitReference(context.Background(), types.CatalogReferenceJob{
		Reference: papers.references[referenceID], Generation: 1,
		Query: types.PaperLookupQuery{Title: "Reference"},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		finals := 0
		for _, payload := range capture.payloads() {
			if payload.Final {
				finals++
			}
		}
		if finals >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("finalized %d/2 catalog jobs", finals)
		}
		time.Sleep(5 * time.Millisecond)
	}

	spans.mu.Lock()
	starts := append([]paperInfoSpanStart(nil), spans.starts...)
	spans.mu.Unlock()
	var sourceSpan, referenceSpan *paperInfoSpanStart
	for index := range starts {
		start := &starts[index]
		if start.kind != types.ProcessingSpanCatalogProvider || start.scope != types.ProcessingSpanScopeProvider {
			continue
		}
		if start.documentID == sourceID {
			sourceSpan = start
		}
		if start.documentID == citingID {
			referenceSpan = start
		}
	}
	if sourceSpan == nil || !strings.Contains(sourceSpan.scopeID.String(), sourceID.String()) {
		t.Fatalf("source provider span = %#v", sourceSpan)
	}
	if referenceSpan == nil || !strings.Contains(referenceSpan.scopeID.String(), referenceID.String()) {
		t.Fatalf("reference provider span = %#v", referenceSpan)
	}
}

// TestCatalogDispatcherProviderPanicFailsAttemptAndSpan verifies that a panic
// finalizes the job as failed without retrying and records a failed span.
func TestCatalogDispatcherProviderPanicFailsAttemptAndSpan(t *testing.T) {
	spans := &paperInfoSpanStub{}
	var calls atomic.Int64
	providers := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			calls.Add(1)
			panic("provider exploded")
		}},
	}}
	capture := &catalogTaskCapture{}
	dispatcher := NewCatalogDispatcher(
		testCatalogDispatcherConfig("crossref"), providers, newPaperInfoPaperServiceStub(), capture, spans, nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	if err := dispatcher.SubmitSource(context.Background(), types.CatalogSourceJob{
		DocumentID: uuid.New(), Generation: 1,
		Query: types.PaperLookupQuery{Title: "Panic Paper"},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var final *types.PaperQueryApplyPayload
		for _, payload := range capture.payloads() {
			if payload.Final && payload.Kind == types.CatalogJobSource {
				copied := payload
				final = &copied
			}
		}
		if final != nil {
			if final.Outcome != types.CatalogOutcomeFailed {
				t.Fatalf("panic outcome = %q, want failed", final.Outcome)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("panicking provider did not finalize the job")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 (no panic retry loop)", got)
	}
	spans.mu.Lock()
	finishes := append([]paperInfoSpanFinish(nil), spans.finishes...)
	spans.mu.Unlock()
	found := false
	for _, finish := range finishes {
		if finish.status == types.ProcessingSpanFailed {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider span finishes = %#v, want a failed span", finishes)
	}
}

type signalingDocumentRepo struct {
	*paperDocumentRepoStub
	stageFinished chan types.DocumentStageState
}

func (r *signalingDocumentRepo) FinishStage(
	ctx context.Context, id uuid.UUID, stage types.DocumentStage, generation int64,
	status types.DocumentStageStatus, code, message string,
) (types.DocumentStageState, bool, error) {
	state, applied, err := r.paperDocumentRepoStub.FinishStage(ctx, id, stage, generation, status, code, message)
	if err == nil && applied && status == types.DocumentStageFailed {
		select {
		case r.stageFinished <- state:
		default:
		}
	}
	return state, applied, err
}

type failingTaskEnqueuer struct{ calls atomic.Int64 }

func (e *failingTaskEnqueuer) EnqueueContext(
	context.Context, *asynq.Task, ...asynq.Option,
) (*asynq.TaskInfo, error) {
	e.calls.Add(1)
	return nil, errors.New("redis unavailable")
}

// TestCatalogDispatcherStopFailsPendingJobs verifies that shutdown fails queued
// source stages and reference rows rather than leaving them pending.
func TestCatalogDispatcherStopFailsPendingJobs(t *testing.T) {
	documentID, citingID, referenceID := uuid.New(), uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
	})
	if _, _, err := documents.QueueStageNewGeneration(context.Background(), documentID, types.DocumentStageSourceLookup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := documents.TryStartStage(context.Background(), documentID, types.DocumentStageSourceLookup, 1); err != nil {
		t.Fatal(err)
	}
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: citingID,
		RawReference: "Reference", Title: "Reference",
		ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
		MetadataSource: types.PaperMetadataSourceExtraction,
	}
	providers := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			return nil, nil
		}},
	}}
	dispatcher := NewCatalogDispatcher(
		testCatalogDispatcherConfig("crossref"), providers, papers, &catalogTaskCapture{}, nil, documents,
	)
	if err := dispatcher.SubmitSource(context.Background(), types.CatalogSourceJob{
		DocumentID: documentID, Generation: 1, Query: types.PaperLookupQuery{Title: "Source"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.SubmitReference(context.Background(), types.CatalogReferenceJob{
		Reference: papers.references[referenceID], Generation: 1,
		Query: types.PaperLookupQuery{Title: "Reference"},
	}); err != nil {
		t.Fatal(err)
	}

	dispatcher.Stop()

	state, err := documents.GetStage(context.Background(), documentID, types.DocumentStageSourceLookup)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != types.DocumentStageFailed {
		t.Fatalf("source stage after Stop = %q, want failed", state.Status)
	}
	papers.mu.Lock()
	reference := papers.references[referenceID]
	papers.mu.Unlock()
	if reference.ResolutionStatus != types.DocumentStageFailed {
		t.Fatalf("reference status after Stop = %q, want failed", reference.ResolutionStatus)
	}
}

// TestCatalogDispatcherEnqueueFailureFailsTerminalJob verifies that persistent
// result-enqueue failures after retries mark the source stage failed.
func TestCatalogDispatcherEnqueueFailureFailsTerminalJob(t *testing.T) {
	documentID := uuid.New()
	baseDocuments := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, DOI: "10.1000/source",
	})
	if _, _, err := baseDocuments.QueueStageNewGeneration(context.Background(), documentID, types.DocumentStageSourceLookup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := baseDocuments.TryStartStage(context.Background(), documentID, types.DocumentStageSourceLookup, 1); err != nil {
		t.Fatal(err)
	}
	documents := &signalingDocumentRepo{
		paperDocumentRepoStub: baseDocuments,
		stageFinished:         make(chan types.DocumentStageState, 1),
	}
	providers := &paperInfoRegistryStub{providers: []interfaces.PaperCatalogProvider{
		&paperInfoProviderStub{name: "crossref", search: func(types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
			return []types.PaperCatalogCandidate{
				testCatalogCandidate("crossref", "10.1000/source", "Source Paper", 1),
			}, nil
		}},
	}}
	enqueuer := &failingTaskEnqueuer{}
	dispatcher := NewCatalogDispatcher(
		testCatalogDispatcherConfig("crossref"), providers, newPaperInfoPaperServiceStub(), enqueuer, nil, documents,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	if err := dispatcher.SubmitSource(context.Background(), types.CatalogSourceJob{
		DocumentID: documentID, Generation: 1,
		Query: types.PaperLookupQuery{DOI: "10.1000/source", Title: "Source Paper"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-documents.stageFinished:
		if state.Status != types.DocumentStageFailed {
			t.Fatalf("stage state = %q, want failed", state.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal job was not failed after result enqueue failure")
	}
	if enqueuer.calls.Load() < 3 {
		t.Fatalf("enqueue attempts = %d, want at least 3", enqueuer.calls.Load())
	}
}

func waitForReference(
	t *testing.T, papers *paperInfoPaperServiceStub, referenceID uuid.UUID,
	want types.DocumentStageStatus,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		papers.mu.Lock()
		reference := papers.references[referenceID]
		papers.mu.Unlock()
		if reference.ResolutionStatus == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reference status = %q, want %q", reference.ResolutionStatus, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from catalog_live_test.go.
// ---------------------------------------------------------------------------

// TestLiveCatalogDispatcher exercises the real public catalog APIs through
// the in-process dispatcher. It is opt-in:
//
//	PAPER_CATALOG_LIVE_TEST=1 CROSSREF_MAILTO=you@example.com go test \
//	  ./internal/application/service -run '^TestLiveCatalogDispatcher$' -v -timeout 5m
func TestLiveCatalogDispatcher(t *testing.T) {
	if os.Getenv("PAPER_CATALOG_LIVE_TEST") != "1" {
		t.Skip("set PAPER_CATALOG_LIVE_TEST=1 to call real catalog APIs")
	}
	const userAgent = "papersManager-dispatcher-live-test/1.0"
	client := func() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

	arxiv, err := papercatalog.NewArXivProvider(client(), userAgent, 0)
	if err != nil {
		t.Fatal(err)
	}
	openAlex, err := papercatalog.NewOpenAlexProvider(client(), os.Getenv("OPENALEX_API_KEY"), userAgent)
	if err != nil {
		t.Fatal(err)
	}
	semanticScholar, err := papercatalog.NewSemanticScholarProvider(client(), os.Getenv("SEMANTIC_SCHOLAR_API_KEY"), userAgent)
	if err != nil {
		t.Fatal(err)
	}
	crossref, err := papercatalog.NewCrossrefProvider(client(), os.Getenv("CROSSREF_MAILTO"), userAgent)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := papercatalog.NewRegistry(
		&liveCountingProvider{inner: arxiv},
		&liveCountingProvider{inner: openAlex},
		&liveCountingProvider{inner: semanticScholar},
		&liveCountingProvider{inner: crossref},
	)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		PaperCatalogProviders:                       []string{"arxiv", "openalex", "semantic_scholar", "crossref"},
		PaperCatalogTimeout:                         20 * time.Second,
		PaperCatalogConcurrency:                     4,
		PaperCatalogProviderTimeouts:                map[string]time.Duration{"arxiv": 20 * time.Second, "openalex": 15 * time.Second, "semantic_scholar": 15 * time.Second, "crossref": 15 * time.Second},
		PaperCatalogProviderSpacings:                map[string]time.Duration{"arxiv": 3 * time.Second, "openalex": time.Second, "semantic_scholar": time.Second, "crossref": 500 * time.Millisecond},
		PaperCatalogProviderConcurrency:             map[string]int{"arxiv": 1, "openalex": 1, "semantic_scholar": 1, "crossref": 1},
		PaperReferenceCatalogConcurrency:            2,
		PaperReferenceCatalogPerDocumentConcurrency: 1,
		PaperCatalogSourceStopScore:                 0.9,
		PaperCatalogTaskTimeout:                     10 * time.Second,
		PaperCatalogTaskMaxRetry:                    1,
	}
	papers := newPaperInfoPaperServiceStub()
	capture := &liveTaskCapture{}
	dispatcher := NewCatalogDispatcher(cfg, registry, papers, capture, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	type sourceCase struct {
		label  string
		query  types.PaperLookupQuery
		expect string
	}
	sourceCases := []sourceCase{
		{
			label:  "Attention Is All You Need (arXiv URL)",
			query:  types.PaperLookupQuery{SourceURL: "https://arxiv.org/abs/1706.03762", Limit: 3},
			expect: "attention",
		},
		{
			label:  "Deep learning (DOI)",
			query:  types.PaperLookupQuery{DOI: "10.1038/nature14539", Limit: 3},
			expect: "deep learning",
		},
		{
			label: "BERT (title + author)",
			query: types.PaperLookupQuery{
				Title:   "BERT: Pre-training of Deep Bidirectional Transformers for Language Understanding",
				Authors: []string{"Jacob Devlin"}, PublishedYear: 2019,
				Limit: 3,
			},
			expect: "bert",
		},
	}
	type tracked struct {
		label      string
		expect     string
		documentID uuid.UUID
		started    time.Time
	}
	trackedCases := make([]tracked, 0, len(sourceCases))
	for _, test := range sourceCases {
		documentID := uuid.New()
		trackedCases = append(trackedCases, tracked{label: test.label, expect: test.expect, documentID: documentID, started: time.Now()})
		if err := dispatcher.SubmitSource(context.Background(), types.CatalogSourceJob{
			DocumentID: documentID, Generation: 1, Query: test.query,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Reference jobs use the same dispatcher and provider workers.
	type referenceCase struct {
		label     string
		reference types.PaperReference
		query     types.PaperLookupQuery
		started   time.Time
	}
	referenceCases := []referenceCase{
		{
			label: "reference Deep learning (DOI)",
			reference: types.PaperReference{
				ReferenceID: uuid.New(), CitingDocumentID: uuid.New(),
				RawReference: "LeCun, Bengio, Hinton. Deep learning. Nature, 2015. 10.1038/nature14539",
				DOI:          "10.1038/nature14539", Title: "Deep learning",
				ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
				MetadataSource: types.PaperMetadataSourceExtraction,
			},
			query: types.PaperLookupQuery{DOI: "10.1038/nature14539", Limit: 3}, started: time.Now(),
		},
		{
			label: "reference Attention (arXiv URL)",
			reference: types.PaperReference{
				ReferenceID: uuid.New(), CitingDocumentID: uuid.New(),
				RawReference: "Vaswani et al. Attention Is All You Need. arXiv:1706.03762",
				SourceURL:    "https://arxiv.org/abs/1706.03762", Title: "Attention Is All You Need",
				ResolutionStatus: types.DocumentStageQueued, ResolutionGeneration: 1,
				MetadataSource: types.PaperMetadataSourceExtraction,
			},
			query: types.PaperLookupQuery{SourceURL: "https://arxiv.org/abs/1706.03762", Limit: 3}, started: time.Now(),
		},
	}
	for _, test := range referenceCases {
		papers.mu.Lock()
		papers.references[test.reference.ReferenceID] = test.reference
		papers.mu.Unlock()
		if err := dispatcher.SubmitReference(context.Background(), types.CatalogReferenceJob{
			Reference: test.reference, Generation: 1, Query: test.query,
		}); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(2 * time.Minute)
	finalSeen := func() map[uuid.UUID]liveCaptured {
		result := make(map[uuid.UUID]liveCaptured)
		for _, captured := range capture.captured() {
			if !captured.payload.Final {
				continue
			}
			id := captured.payload.DocumentID
			if captured.payload.Kind == types.CatalogJobReference {
				id = captured.payload.ReferenceID
			}
			parsed, err := uuid.Parse(id)
			if err != nil {
				continue
			}
			if _, exists := result[parsed]; !exists {
				result[parsed] = captured
			}
		}
		return result
	}
	for {
		finals := finalSeen()
		done := true
		for _, test := range trackedCases {
			if _, ok := finals[test.documentID]; !ok {
				done = false
				break
			}
		}
		for _, test := range referenceCases {
			if _, ok := finals[test.reference.ReferenceID]; !ok {
				done = false
				break
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live dispatcher did not finish all jobs; finals=%d", len(finals))
		}
		time.Sleep(50 * time.Millisecond)
	}

	finals := finalSeen()
	for _, test := range trackedCases {
		captured := finals[test.documentID]
		logLiveResult(t, test.label, captured.at.Sub(test.started), captured.payload)
	}
	for _, test := range referenceCases {
		captured := finals[test.reference.ReferenceID]
		logLiveResult(t, test.label, captured.at.Sub(test.started), captured.payload)
	}
	for _, call := range registryCalls(registry) {
		t.Logf("provider call provider=%s title=%q doi=%q url=%q latency=%s err=%v",
			call.provider, call.title, call.doi, call.sourceURL, call.latency, call.err)
	}
}

func logLiveResult(t *testing.T, label string, elapsed time.Duration, payload types.PaperQueryApplyPayload) {
	t.Helper()
	title, doi := "", ""
	if payload.Candidate != nil {
		title, doi = payload.Candidate.Title, payload.Candidate.DOI
	}
	t.Logf("result %q: outcome=%s provider=%s elapsed=%s title=%q doi=%q",
		label, payload.Outcome, payload.Provider, elapsed.Round(time.Millisecond), title, doi)
}

type liveCountingProvider struct {
	inner interfaces.PaperCatalogProvider
	mu    sync.Mutex
	calls []liveProviderCall
}

type liveProviderCall struct {
	provider  string
	title     string
	doi       string
	sourceURL string
	latency   time.Duration
	err       error
}

func (p *liveCountingProvider) Name() string { return p.inner.Name() }

func (p *liveCountingProvider) Search(ctx context.Context, query types.PaperLookupQuery) ([]types.PaperCatalogCandidate, error) {
	started := time.Now()
	candidates, err := p.inner.Search(ctx, query)
	p.mu.Lock()
	p.calls = append(p.calls, liveProviderCall{
		provider: p.inner.Name(), title: query.Title,
		doi: query.DOI, sourceURL: query.SourceURL,
		latency: time.Since(started), err: err,
	})
	p.mu.Unlock()
	return candidates, err
}

func registryCalls(registry interfaces.PaperCatalogRegistry) []liveProviderCall {
	var calls []liveProviderCall
	for _, provider := range registry.Providers() {
		if counting, ok := provider.(*liveCountingProvider); ok {
			counting.mu.Lock()
			calls = append(calls, counting.calls...)
			counting.mu.Unlock()
		}
	}
	return calls
}

type liveCaptured struct {
	payload types.PaperQueryApplyPayload
	at      time.Time
}

type liveTaskCapture struct {
	mu      sync.Mutex
	results []liveCaptured
}

func (c *liveTaskCapture) EnqueueContext(
	_ context.Context, task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	if task.Type() == types.TypePaperQueryApply {
		var payload types.PaperQueryApplyPayload
		if err := json.Unmarshal(task.Payload(), &payload); err == nil {
			c.mu.Lock()
			c.results = append(c.results, liveCaptured{payload: payload, at: time.Now()})
			c.mu.Unlock()
		}
	}
	return &asynq.TaskInfo{ID: "live"}, nil
}

func (c *liveTaskCapture) captured() []liveCaptured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]liveCaptured(nil), c.results...)
}
