package types

import (
	"testing"

	"github.com/google/uuid"
)

func TestComposeProcessingSpanScopeID(t *testing.T) {
	runID := uuid.New()
	scopeID := ComposeProcessingSpanScopeID(
		ProcessingSpanCatalogReference,
		runID.String(),
		NormalizePaperCatalogProvider(" OpenAlex ").String(),
	)
	if got, want := scopeID.String(), "catalog_reference:"+runID.String()+":openalex"; got != want {
		t.Fatalf("ComposeProcessingSpanScopeID() = %q, want %q", got, want)
	}
	parts := SplitProcessingSpanScopeID(scopeID)
	if len(parts) != 3 || parts[0] != string(ProcessingSpanCatalogReference) || parts[1] != runID.String() || parts[2] != PaperCatalogProviderOpenAlex.String() {
		t.Fatalf("SplitProcessingSpanScopeID() = %#v", parts)
	}
}

func TestProcessingSpanScopeIDHelpersKeepTargetsSeparate(t *testing.T) {
	sourceID, citedID, runID := uuid.New(), uuid.New(), uuid.New()
	source := ComposeProcessingSpanScopeID(
		ProcessingSpanCatalogProvider,
		ComposeProcessingSpanScopeID(ProcessingSpanCatalogSource, sourceID.String()).String(),
		PaperCatalogProviderArXiv.String(),
	)
	reference := ComposeProcessingSpanScopeID(
		ProcessingSpanCatalogProvider,
		ComposeProcessingSpanScopeID(ProcessingSpanCatalogReference, runID.String(), citedID.String()).String(),
		PaperCatalogProviderArXiv.String(),
	)
	if source == reference {
		t.Fatalf("source and reference provider scope IDs must differ: %q", source)
	}
	if got, want := ComposeProcessingSpanScopeID(ProcessingSpanCatalogReference, runID.String()).String(), "catalog_reference:"+runID.String(); got != want {
		t.Fatalf("run scope ID = %q, want %q", got, want)
	}
}

func TestSplitProcessingSpanScopeIDEmpty(t *testing.T) {
	if parts := SplitProcessingSpanScopeID(""); parts != nil {
		t.Fatalf("SplitProcessingSpanScopeID(empty) = %#v, want nil", parts)
	}
}
