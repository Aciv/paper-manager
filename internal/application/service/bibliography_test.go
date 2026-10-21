package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

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
