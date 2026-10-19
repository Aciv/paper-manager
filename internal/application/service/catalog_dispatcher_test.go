package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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
