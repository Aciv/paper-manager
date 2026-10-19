package service

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/infrastructure/papercatalog"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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
