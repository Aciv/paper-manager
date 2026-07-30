package papercatalog

import (
	"context"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestMemoryReferenceSchedulerUsesSharedRotationAndFallback(t *testing.T) {
	scheduler := &memoryReferenceScheduler{
		jobs: make(map[uuid.UUID]types.PaperReferenceJob),
		providers: []types.PaperCatalogProvider{
			types.PaperCatalogProviderArXiv,
			types.PaperCatalogProviderOpenAlex,
			types.PaperCatalogProviderCrossref,
		},
	}
	source, run := uuid.New(), uuid.New()
	first, second := uuid.New(), uuid.New()
	for _, target := range []uuid.UUID{first, second} {
		queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target, Owners: []types.PaperReferenceOwner{{SourceDocumentID: source, RunID: run}}})
		if err != nil || !queued {
			t.Fatalf("Register(%s) = %v, %v", target, queued, err)
		}
	}
	claimOne, err := scheduler.Claim(context.Background(), first)
	if err != nil || !claimOne.Ready() {
		t.Fatalf("first claim = %#v, %v", claimOne, err)
	}
	claimTwo, err := scheduler.Claim(context.Background(), second)
	if err != nil || !claimTwo.Ready() {
		t.Fatalf("second claim = %#v, %v", claimTwo, err)
	}
	if claimOne.Provider == claimTwo.Provider {
		t.Fatalf("shared rotation assigned %q twice", claimOne.Provider)
	}
	report, err := scheduler.Report(context.Background(), first, claimOne.Provider, types.PaperReferenceOutcomeNoMatch)
	if err != nil || !report.Requeue {
		t.Fatalf("fallback report = %#v, %v", report, err)
	}
	fallback, err := scheduler.Claim(context.Background(), first)
	if err != nil || !fallback.Ready() || fallback.Provider == claimOne.Provider {
		t.Fatalf("fallback claim = %#v, %v", fallback, err)
	}
}

func TestMemoryReferenceSchedulerCancelsOnlyOneRun(t *testing.T) {
	scheduler := &memoryReferenceScheduler{jobs: make(map[uuid.UUID]types.PaperReferenceJob), providers: []types.PaperCatalogProvider{types.PaperCatalogProviderCrossref}}
	source, firstRun, secondRun := uuid.New(), uuid.New(), uuid.New()
	first, second := uuid.New(), uuid.New()
	for target, run := range map[uuid.UUID]uuid.UUID{first: firstRun, second: secondRun} {
		if _, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target, Owners: []types.PaperReferenceOwner{{SourceDocumentID: source, RunID: run}}}); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := scheduler.CancelRun(context.Background(), source, firstRun)
	if err != nil || len(ids) != 1 || ids[0] != first {
		t.Fatalf("CancelRun() = %#v, %v", ids, err)
	}
	active, err := scheduler.IsRunActive(context.Background(), source, secondRun)
	if err != nil || !active {
		t.Fatalf("second run active = %v, %v", active, err)
	}
	claim, err := scheduler.Claim(context.Background(), first)
	if err != nil || claim.Ready() || !claim.Job.Cancelled {
		t.Fatalf("cancelled claim = %#v, %v", claim, err)
	}
}

func TestMemoryReferenceSchedulerKeepsSharedTargetForOtherOwner(t *testing.T) {
	scheduler := &memoryReferenceScheduler{jobs: make(map[uuid.UUID]types.PaperReferenceJob), providers: []types.PaperCatalogProvider{types.PaperCatalogProviderCrossref}}
	target, firstSource, secondSource := uuid.New(), uuid.New(), uuid.New()
	firstRun, secondRun := uuid.New(), uuid.New()
	if _, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target, Owners: []types.PaperReferenceOwner{{SourceDocumentID: firstSource, RunID: firstRun}}}); err != nil {
		t.Fatal(err)
	}
	if queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target, Owners: []types.PaperReferenceOwner{{SourceDocumentID: secondSource, RunID: secondRun}}}); err != nil || queued {
		t.Fatalf("shared register = %v, %v", queued, err)
	}
	ids, err := scheduler.CancelRun(context.Background(), firstSource, firstRun)
	if err != nil || len(ids) != 0 {
		t.Fatalf("first cancellation should retain target: %#v, %v", ids, err)
	}
	active, err := scheduler.IsTargetActive(context.Background(), target)
	if err != nil || !active {
		t.Fatalf("shared target active = %v, %v", active, err)
	}
	progress, err := scheduler.Progress(context.Background(), secondSource)
	if err != nil || progress.Queued != 1 {
		t.Fatalf("second-source progress = %#v, %v", progress, err)
	}
}
