package papercatalog

import (
	"context"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestReferenceSchedulerDeduplicatesActiveTarget(t *testing.T) {
	scheduler := newMemoryReferenceScheduler(configuredReferenceProviders(config.Config{}))
	target := uuid.New()
	if queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target}); err != nil || !queued {
		t.Fatalf("first Register() queued=%t err=%v", queued, err)
	}
	if queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target}); err != nil || queued {
		t.Fatalf("duplicate Register() queued=%t err=%v", queued, err)
	}
}

func TestReferenceSchedulerNewGenerationReplacesStaleActiveTarget(t *testing.T) {
	scheduler := newMemoryReferenceScheduler([]types.PaperCatalogProvider{
		types.PaperCatalogProviderArXiv,
		types.PaperCatalogProviderOpenAlex,
	})
	target := uuid.New()
	if queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{
		TargetDocumentID: target,
		Generation:       1,
	}); err != nil || !queued {
		t.Fatalf("first Register() queued=%t err=%v", queued, err)
	}
	first, err := scheduler.Claim(context.Background(), target)
	if err != nil || !first.Ready() {
		t.Fatalf("first Claim()=%#v err=%v", first, err)
	}
	if queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{
		TargetDocumentID: target,
		Generation:       1,
	}); err != nil || queued {
		t.Fatalf("same generation Register() queued=%t err=%v", queued, err)
	}
	if queued, err := scheduler.Register(context.Background(), types.PaperReferenceJob{
		TargetDocumentID: target,
		Generation:       2,
	}); err != nil || !queued {
		t.Fatalf("new generation Register() queued=%t err=%v", queued, err)
	}
	second, err := scheduler.Claim(context.Background(), target)
	if err != nil || !second.Ready() || second.Job.Generation != 2 {
		t.Fatalf("replacement Claim()=%#v err=%v", second, err)
	}
}

func TestReferenceSchedulerFallsBackPerTarget(t *testing.T) {
	scheduler := newMemoryReferenceScheduler([]types.PaperCatalogProvider{
		types.PaperCatalogProviderArXiv, types.PaperCatalogProviderOpenAlex,
	})
	target := uuid.New()
	if _, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target}); err != nil {
		t.Fatal(err)
	}
	first, err := scheduler.Claim(context.Background(), target)
	if err != nil || !first.Ready() {
		t.Fatalf("first Claim()=%#v err=%v", first, err)
	}
	report, err := scheduler.Report(context.Background(), target, first.Provider, types.PaperReferenceOutcomeNoMatch)
	if err != nil || !report.Requeue {
		t.Fatalf("first Report()=%#v err=%v", report, err)
	}
	second, err := scheduler.Claim(context.Background(), target)
	if err != nil || !second.Ready() || second.Provider == first.Provider {
		t.Fatalf("fallback Claim()=%#v err=%v", second, err)
	}
}

func TestReferenceSchedulerCooldownAvoidsProvider(t *testing.T) {
	scheduler := newMemoryReferenceScheduler([]types.PaperCatalogProvider{
		types.PaperCatalogProviderArXiv, types.PaperCatalogProviderOpenAlex,
	})
	if err := scheduler.Cooldown(context.Background(), types.PaperCatalogProviderArXiv, time.Minute); err != nil {
		t.Fatal(err)
	}
	target := uuid.New()
	if _, err := scheduler.Register(context.Background(), types.PaperReferenceJob{TargetDocumentID: target}); err != nil {
		t.Fatal(err)
	}
	claim, err := scheduler.Claim(context.Background(), target)
	if err != nil || claim.Provider != types.PaperCatalogProviderOpenAlex {
		t.Fatalf("Claim()=%#v err=%v", claim, err)
	}
}
