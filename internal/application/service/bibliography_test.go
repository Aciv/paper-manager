package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from bibliography_test.go.
// ---------------------------------------------------------------------------

type bibliographyRepositoryStub struct {
	snapshot types.BibliographySnapshotSource
	err      error
}

func (stub *bibliographyRepositoryStub) GetBibliographySnapshot(context.Context, uuid.UUID, int) (types.BibliographySnapshotSource, error) {
	return stub.snapshot, stub.err
}

type doiCitationStub struct {
	responses map[string]types.DOICitation
	delays    map[string]time.Duration
	errors    map[string]error
}

type measuringDOIStub struct {
	mu        sync.Mutex
	active    int
	maxActive int
	delay     time.Duration
}

func (stub *measuringDOIStub) FetchBibTeX(ctx context.Context, doi string) (types.DOICitation, error) {
	stub.mu.Lock()
	stub.active++
	if stub.active > stub.maxActive {
		stub.maxActive = stub.active
	}
	stub.mu.Unlock()
	defer func() {
		stub.mu.Lock()
		stub.active--
		stub.mu.Unlock()
	}()
	select {
	case <-time.After(stub.delay):
		return types.DOICitation{BibTeX: "@misc{" + strings.ReplaceAll(doi, "/", "-") + ",title={Remote}}", CitationKey: strings.ReplaceAll(doi, "/", "-")}, nil
	case <-ctx.Done():
		return types.DOICitation{}, &types.DOICitationError{Code: "timeout", Err: ctx.Err()}
	}
}

func (stub *doiCitationStub) FetchBibTeX(ctx context.Context, doi string) (types.DOICitation, error) {
	if delay := stub.delays[doi]; delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return types.DOICitation{}, &types.DOICitationError{Code: "timeout", Err: ctx.Err()}
		}
	}
	return stub.responses[doi], stub.errors[doi]
}

// TestGenerateBaseBibliographyPreservesOrderAndFallsBack verifies that entries keep their
// source order despite out-of-order DOI completion and that colliding keys get local suffixes.
func TestGenerateBaseBibliographyPreservesOrderAndFallsBack(t *testing.T) {
	baseID := uuid.New()
	published := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sources := []types.BibliographySource{
		{DocumentID: uuid.New(), DOI: "10.1000/slow", SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Remote slow"}},
		{DocumentID: uuid.New(), SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Code work", Authors: []types.PaperAuthor{{Name: "Ada Smith"}}, PublishedAt: &published}},
		{DocumentID: uuid.New(), DOI: "10.1000/fast", SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Remote fast"}},
	}
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID, Name: "My Base"}, Entries: sources}},
		&doiCitationStub{
			responses: map[string]types.DOICitation{
				"10.1000/slow": {BibTeX: "@article{smith2024code, title={Slow}}", CitationKey: "smith2024code"},
				"10.1000/fast": {BibTeX: "@article{smith2024code, title={Fast}}", CitationKey: "smith2024code"},
			},
			delays: map[string]time.Duration{"10.1000/slow": 25 * time.Millisecond}, errors: map[string]error{},
		},
		config.Config{BibliographyMaxEntries: 20, BibliographyDOIConcurrency: 2, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	result, err := service.generateBaseBibliography(context.Background(), baseID.String())
	if err != nil {
		t.Fatal(err)
	}
	if result.Generated != 3 || result.Failed != 0 || result.Entries[1].CitationKey != "smith2024code-a" {
		t.Fatalf("unexpected report: %#v", result)
	}
	if result.Entries[0].Warnings[0] != "duplicate_citation_key" || result.Entries[2].Warnings[0] != "duplicate_citation_key" {
		t.Fatalf("duplicate warnings missing: %#v", result.Entries)
	}
	slow := strings.Index(result.BibTeX, "title={Slow}")
	local := strings.Index(result.BibTeX, "@misc{smith2024code-a")
	fast := strings.Index(result.BibTeX, "title={Fast}")
	if !(slow >= 0 && slow < local && local < fast) {
		t.Fatalf("output order changed with completion order:\n%s", result.BibTeX)
	}
	if len(result.ExportEntries) != 3 {
		t.Fatalf("structured entries = %#v", result.ExportEntries)
	}
	wantSources := []string{types.BibliographyExportSourceDOI, types.BibliographyExportSourceAuto, types.BibliographyExportSourceDOI}
	for index, entry := range result.ExportEntries {
		if entry.Source != wantSources[index] || entry.Kind != types.BibliographyExportKindArticle || entry.BibTeX == "" {
			t.Fatalf("structured entry %d = %#v", index, entry)
		}
	}
}

// TestGenerateBaseBibliographyFallsBackAfterDOIFailure verifies that a failed DOI lookup
// produces a local @misc fallback while recording the provider error code.
func TestGenerateBaseBibliographyFallsBackAfterDOIFailure(t *testing.T) {
	baseID := uuid.New()
	source := types.BibliographySource{DocumentID: uuid.New(), DOI: "10.1000/fail", SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Local title"}}
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID, Name: "Fallback"}, Entries: []types.BibliographySource{source}}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, delays: map[string]time.Duration{}, errors: map[string]error{"10.1000/fail": &types.DOICitationError{Code: "http_404", StatusCode: 404}}},
		config.Config{BibliographyMaxEntries: 20, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	result, err := service.generateBaseBibliography(context.Background(), baseID.String())
	if err != nil {
		t.Fatal(err)
	}
	report := result.Entries[0]
	if report.Source != types.BibliographySourceLocalDOIFailed || report.DOIErrorCode != "http_404" || !strings.HasPrefix(result.BibTeX, "@misc{") {
		t.Fatalf("unexpected fallback: %#v\n%s", report, result.BibTeX)
	}
	if len(result.ExportEntries) != 1 || result.ExportEntries[0].Source != types.BibliographyExportSourceAuto || result.ExportEntries[0].BibTeX != result.BibTeX {
		t.Fatalf("fallback structured entry = %#v", result.ExportEntries)
	}
}

// TestGenerateBaseBibliographyAllocatesStableLocalCollisionSuffixes verifies that documents
// with identical metadata receive deterministic, ordered citation-key suffixes.
func TestGenerateBaseBibliographyAllocatesStableLocalCollisionSuffixes(t *testing.T) {
	baseID := uuid.New()
	metadata := types.PaperMetadata{Title: "Same title", Authors: []types.PaperAuthor{{Name: "Ada Smith"}}}
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{
			Base: types.DocumentBase{ID: baseID, Name: "Local collisions"},
			Entries: []types.BibliographySource{
				{DocumentID: uuid.New(), SourceState: types.DocumentSourceAvailable, Metadata: metadata},
				{DocumentID: uuid.New(), SourceState: types.DocumentSourceAvailable, Metadata: metadata},
			},
		}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, delays: map[string]time.Duration{}, errors: map[string]error{}},
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	result, err := service.generateBaseBibliography(context.Background(), baseID.String())
	if err != nil {
		t.Fatal(err)
	}
	if result.Entries[0].CitationKey != "smithndsame" || result.Entries[1].CitationKey != "smithndsame-a" {
		t.Fatalf("keys = %q, %q", result.Entries[0].CitationKey, result.Entries[1].CitationKey)
	}
}

// TestGenerateBaseBibliographyRejectsInvalidBaseAndLargeOutput verifies that a malformed
// base ID and output exceeding the configured byte limit are both rejected.
func TestGenerateBaseBibliographyRejectsInvalidBaseAndLargeOutput(t *testing.T) {
	service := NewBibliographyService(&bibliographyRepositoryStub{}, &doiCitationStub{}, config.Config{BibliographyMaxEntries: 1, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1})
	if _, err := service.generateBaseBibliography(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("expected invalid base id")
	}
	baseID := uuid.New()
	service = NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID, Name: "Large"}, Entries: []types.BibliographySource{{DocumentID: uuid.New(), SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Title"}}}}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, errors: map[string]error{}, delays: map[string]time.Duration{}},
		config.Config{BibliographyMaxEntries: 1, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1},
	)
	if _, err := service.generateBaseBibliography(context.Background(), baseID.String()); !errors.Is(err, types.ErrBibliographyOutputTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

// TestGenerateBaseBibliographyBoundsConcurrencyAndTotalBudget verifies that DOI lookups
// respect the concurrency limit and total timeout, falling back once the budget expires.
func TestGenerateBaseBibliographyBoundsConcurrencyAndTotalBudget(t *testing.T) {
	baseID := uuid.New()
	sources := make([]types.BibliographySource, 6)
	for index := range sources {
		sources[index] = types.BibliographySource{
			DocumentID: uuid.New(), DOI: "10.1000/slow-" + string(rune('a'+index)),
			SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Fallback title"},
		}
	}
	doi := &measuringDOIStub{delay: 200 * time.Millisecond}
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID, Name: "Bounded"}, Entries: sources}},
		doi,
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 2, BibliographyTotalTimeout: 30 * time.Millisecond, BibliographyMaxOutputBytes: 1 << 20},
	)
	started := time.Now()
	result, err := service.generateBaseBibliography(context.Background(), baseID.String())
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("total lookup budget was not enforced: %s", elapsed)
	}
	if doi.maxActive > 2 {
		t.Fatalf("max active DOI requests = %d, want <= 2", doi.maxActive)
	}
	if result.Generated != len(sources) {
		t.Fatalf("generated = %d, want %d", result.Generated, len(sources))
	}
	for _, report := range result.Entries {
		if report.Source != types.BibliographySourceLocalDOIFailed || report.DOIErrorCode != "timeout" {
			t.Fatalf("unexpected budget fallback: %#v", report)
		}
	}
}

// TestGenerateBaseBibliographyReportsUnavailableAndInsufficientEntries verifies that
// deleting documents are skipped and entries without usable metadata are reported as failed.
func TestGenerateBaseBibliographyReportsUnavailableAndInsufficientEntries(t *testing.T) {
	baseID := uuid.New()
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{
			Base: types.DocumentBase{ID: baseID, Name: "Reports"},
			Entries: []types.BibliographySource{
				{DocumentID: uuid.New(), SourceState: types.DocumentSourceDeleting, Metadata: types.PaperMetadata{Title: "Deleting"}},
				{DocumentID: uuid.New(), SourceState: types.DocumentSourceAvailable},
			},
		}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, errors: map[string]error{}, delays: map[string]time.Duration{}},
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	result, err := service.generateBaseBibliography(context.Background(), baseID.String())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Failed != 1 || result.Generated != 0 {
		t.Fatalf("unexpected totals: %#v", result)
	}
	if result.Entries[0].Outcome != types.BibliographyEntrySkipped || result.Entries[0].Warnings[0] != "document_unavailable" {
		t.Fatalf("unexpected skipped report: %#v", result.Entries[0])
	}
	if result.Entries[1].Outcome != types.BibliographyEntryFailed || result.Entries[1].Warnings[0] != "local_metadata_insufficient" {
		t.Fatalf("unexpected failed report: %#v", result.Entries[1])
	}
}

// TestGenerateBaseBibliographyPropagatesParentCancellation verifies that an already
// cancelled parent context aborts generation with context.Canceled.
func TestGenerateBaseBibliographyPropagatesParentCancellation(t *testing.T) {
	baseID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID, Name: "Cancelled"}, Entries: []types.BibliographySource{{DocumentID: uuid.New(), DOI: "10.1000/cancel", SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Fallback"}}}}},
		&measuringDOIStub{delay: time.Second},
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	if _, err := service.generateBaseBibliography(ctx, baseID.String()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

// TestGenerateBaseBibliographyBoundsJSONResponse verifies that a result exceeding the JSON
// response byte budget is rejected with ErrBibliographyOutputTooLarge.
func TestGenerateBaseBibliographyBoundsJSONResponse(t *testing.T) {
	baseID := uuid.New()
	service := NewBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID, Name: "Response"}, Entries: []types.BibliographySource{{DocumentID: uuid.New(), SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "Title"}}}}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, errors: map[string]error{}, delays: map[string]time.Duration{}},
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20, BibliographyMaxResponseBytes: 1},
	)
	if _, err := service.generateBaseBibliography(context.Background(), baseID.String()); !errors.Is(err, types.ErrBibliographyOutputTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from bibliography_render_test.go.
// ---------------------------------------------------------------------------

// TestRenderLocalBibliographyEntryEscapesFieldsAndBuildsStableKey verifies that local
// entries use a stable key and escape BibTeX-special characters in every field.
func TestRenderLocalBibliographyEntryEscapesFieldsAndBuildsStableKey(t *testing.T) {
	published := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	metadata := types.PaperMetadata{
		Title: "Design & Code_100%", Authors: []types.PaperAuthor{{Name: "Ada Lovelace"}},
		PublishedAt: &published, Venue: "UI {Systems}", SourceURL: "https://example.test/a?x=1&y=2",
	}
	if key := bibliographyLocalKeyBase(metadata); key != "lovelace2024design" {
		t.Fatalf("key = %q", key)
	}
	entry, err := renderLocalBibliographyEntry("lovelace2024design", "10.1000/example", metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`@misc{lovelace2024design,`, `Design \& Code\_100\%`,
		`UI \{Systems\}`, `10.1000/example`, `x=1\&y=2`,
	} {
		if !strings.Contains(entry, fragment) {
			t.Fatalf("entry missing %q:\n%s", fragment, entry)
		}
	}
}

// TestRenderLocalBibliographyEntryRequiresTitle verifies that rendering fails when the
// metadata has no title.
func TestRenderLocalBibliographyEntryRequiresTitle(t *testing.T) {
	if _, err := renderLocalBibliographyEntry("missing", "", types.PaperMetadata{}); err == nil {
		t.Fatal("expected missing-title failure")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from bibliography_task_test.go.
// ---------------------------------------------------------------------------

type bibliographyTaskEnqueuerStub struct {
	task *asynq.Task
	err  error
}

func (stub *bibliographyTaskEnqueuerStub) EnqueueContext(
	_ context.Context, task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	stub.task = task
	if stub.err != nil {
		return nil, stub.err
	}
	return &asynq.TaskInfo{ID: bibliographyTaskIDPrefix + uuid.NewString()}, nil
}

type taskResultInspectorStub struct {
	record types.TaskRecord
	err    error
	queue  string
}

func (stub *taskResultInspectorStub) GetTask(
	_ context.Context, queue, _ string,
) (types.TaskRecord, error) {
	stub.queue = queue
	return stub.record, stub.err
}

// TestEnqueueBaseBibliographyPublishesProjectTask verifies that enqueueing publishes a
// bibliography-generation task carrying the base, request ID, and a positive attempt number.
func TestEnqueueBaseBibliographyPublishesProjectTask(t *testing.T) {
	baseID := uuid.New()
	enqueuer := &bibliographyTaskEnqueuerStub{}
	service := newBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{Base: types.DocumentBase{ID: baseID}}},
		&doiCitationStub{}, enqueuer, nil, nil,
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1},
	)
	result, err := service.EnqueueBaseBibliography(context.Background(), baseID.String(), "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != types.BibliographyTaskQueued || !validBibliographyTaskID(result.ID) {
		t.Fatalf("result = %#v", result)
	}
	if enqueuer.task == nil || enqueuer.task.Type() != types.TypeBibliographyGenerate {
		t.Fatalf("task = %#v", enqueuer.task)
	}
	var payload types.BibliographyTaskPayload
	if err := json.Unmarshal(enqueuer.task.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.DocumentBaseID != baseID.String() || payload.RequestID != "request-1" || payload.Attempt < 1 {
		t.Fatalf("payload = %#v", payload)
	}
}

// TestGetBaseBibliographyTaskReturnsRetainedResult verifies that a completed task's snapshot
// is read from the low queue, while cross-base and wrong-type lookups are rejected.
func TestGetBaseBibliographyTaskReturnsRetainedResult(t *testing.T) {
	baseID := uuid.New()
	runID := uuid.New()
	taskID := bibliographyTaskIDPrefix + runID.String()
	payload, _ := json.Marshal(types.BibliographyTaskPayload{
		DocumentBaseID: baseID.String(), RunID: runID.String(), Attempt: 1,
	})
	snapshot := types.BibliographySnapshot{BaseID: baseID, BaseName: "Base", BibTeX: "@misc{x}"}
	encoded, _ := json.Marshal(snapshot)
	inspector := &taskResultInspectorStub{record: types.TaskRecord{
		ID: taskID, Type: types.TypeBibliographyGenerate, State: "completed", Payload: payload, Result: encoded,
	}}
	service := newBibliographyService(&bibliographyRepositoryStub{}, &doiCitationStub{}, nil, inspector, nil, config.Config{})
	result, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != types.BibliographyTaskSucceeded || result.Snapshot == nil || result.Snapshot.BibTeX != "@misc{x}" {
		t.Fatalf("result = %#v", result)
	}
	if inspector.queue != types.QueueLow {
		t.Fatalf("inspector queue = %q", inspector.queue)
	}
	if _, err := service.GetBaseBibliographyTask(context.Background(), uuid.NewString(), taskID); err == nil {
		t.Fatal("expected cross-base task lookup rejection")
	}
	inspector.record.Type = types.TypeDocumentRecognize
	if _, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID); !errors.Is(err, types.ErrBibliographyTaskNotFound) {
		t.Fatalf("wrong task type error = %v", err)
	}
}

// TestGetBaseBibliographyTaskMapsGenericInspectorErrors verifies that inspector failures are
// translated to ErrBibliographyTaskUnavailable and ErrBibliographyTaskNotFound.
func TestGetBaseBibliographyTaskMapsGenericInspectorErrors(t *testing.T) {
	baseID := uuid.New()
	taskID := bibliographyTaskIDPrefix + uuid.NewString()
	inspector := &taskResultInspectorStub{err: types.ErrTaskInspectorUnavailable}
	service := newBibliographyService(&bibliographyRepositoryStub{}, &doiCitationStub{}, nil, inspector, nil, config.Config{})
	if _, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID); !errors.Is(err, types.ErrBibliographyTaskUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
	inspector.err = types.ErrTaskNotFound
	if _, err := service.GetBaseBibliographyTask(context.Background(), baseID.String(), taskID); !errors.Is(err, types.ErrBibliographyTaskNotFound) {
		t.Fatalf("not found error = %v", err)
	}
}

// TestProcessBaseBibliographyWritesResultAndFinishesDocumentSpans verifies that processing
// writes the snapshot and records per-document spans with matching terminal statuses.
func TestProcessBaseBibliographyWritesResultAndFinishesDocumentSpans(t *testing.T) {
	baseID := uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	spans := &paperInfoSpanStub{}
	service := newBibliographyService(
		&bibliographyRepositoryStub{snapshot: types.BibliographySnapshotSource{
			Base: types.DocumentBase{ID: baseID, Name: "Async"},
			Entries: []types.BibliographySource{
				{DocumentID: firstID, SourceState: types.DocumentSourceAvailable, Metadata: types.PaperMetadata{Title: "First"}},
				{DocumentID: secondID, SourceState: types.DocumentSourceAvailable},
			},
		}},
		&doiCitationStub{responses: map[string]types.DOICitation{}, errors: map[string]error{}, delays: map[string]time.Duration{}},
		nil, nil, spans,
		config.Config{BibliographyMaxEntries: 10, BibliographyDOIConcurrency: 1, BibliographyTotalTimeout: time.Second, BibliographyMaxOutputBytes: 1 << 20},
	)
	payload := types.BibliographyTaskPayload{
		DocumentBaseID: baseID.String(), RunID: uuid.NewString(), Attempt: 7,
	}
	var output bytes.Buffer
	if err := service.processBaseBibliographyPayload(context.Background(), payload, &output); err != nil {
		t.Fatal(err)
	}
	var snapshot types.BibliographySnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Generated != 1 || snapshot.Failed != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if len(spans.starts) != 2 || len(spans.finishes) != 2 {
		t.Fatalf("span starts=%#v finishes=%#v", spans.starts, spans.finishes)
	}
	for _, started := range spans.starts {
		if started.kind != types.ProcessingSpanBibliographyExport || started.scope != types.ProcessingSpanScopeBibliographyExport || started.attempt != 7 {
			t.Fatalf("span start = %#v", started)
		}
	}
	statuses := map[types.ProcessingSpanStatus]int{}
	for _, finished := range spans.finishes {
		statuses[finished.status]++
	}
	if statuses[types.ProcessingSpanSucceeded] != 1 || statuses[types.ProcessingSpanFailed] != 1 {
		t.Fatalf("span statuses = %#v", statuses)
	}
}
