package types

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
)

func TestPaperLookupQueryValidateAndEffectiveLimit(t *testing.T) {
	query := PaperLookupQuery{
		Title:   "A normalized paper title",
		Authors: []string{"Ada Lovelace"},
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("valid catalog query rejected: %v", err)
	}
	if got := query.EffectiveLimit(); got != DefaultPaperCatalogLimit {
		t.Fatalf("EffectiveLimit() = %d, want %d", got, DefaultPaperCatalogLimit)
	}

	query.Limit = 5
	if got := query.EffectiveLimit(); got != 5 {
		t.Fatalf("EffectiveLimit() = %d, want 5", got)
	}
}

func TestPaperLookupQueryRequiresLookupIdentifier(t *testing.T) {
	err := (PaperLookupQuery{}).Validate()
	if !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected ErrInvalidPaperCatalogData, got %v", err)
	}

	query := PaperLookupQuery{SourceURL: "https://arxiv.org/abs/2401.01234"}
	if err := query.Validate(); err != nil {
		t.Fatalf("source URL query rejected: %v", err)
	}
}

func TestPaperCatalogCandidateValidate(t *testing.T) {
	candidate := PaperCatalogCandidate{
		Provider:      "openalex",
		ProviderID:    "W123",
		Title:         "A Paper",
		Authors:       []PaperAuthor{{Name: "Ada Lovelace"}},
		Tags:          []string{"computer science"},
		ProviderScore: 0.8,
		MatchScore:    0.9,
		RawMetadata:   JSON(`{"id":"W123"}`),
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("valid catalog candidate rejected: %v", err)
	}

	candidate.RawMetadata = JSON(`{`)
	if err := candidate.Validate(); !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected invalid raw metadata error, got %v", err)
	}

	candidate.RawMetadata = nil
	candidate.ProviderScore = math.NaN()
	if err := candidate.Validate(); !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected invalid NaN score error, got %v", err)
	}
}

func TestPaperCatalogResolveCommandValidate(t *testing.T) {
	command := PaperCatalogResolveCommand{
		DocumentID: uuid.New(),
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("valid catalog command rejected: %v", err)
	}

	command.DocumentID = uuid.Nil
	if err := command.Validate(); !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected document id validation error, got %v", err)
	}
}

func TestPaperCatalogResolveCommandAllowsUnknownWithoutRevision(t *testing.T) {
	command := PaperCatalogResolveCommand{DocumentID: uuid.New()}
	if err := command.Validate(); err != nil {
		t.Fatalf("source-free catalog command rejected: %v", err)
	}
}

func TestPaperCatalogStatusIsValid(t *testing.T) {
	if !PaperCatalogStatusSuccess.IsValid() {
		t.Fatal("success catalog status should be valid")
	}
	if !PaperCatalogStatusFailed.IsValid() {
		t.Fatal("failed catalog status should be valid")
	}
	if PaperCatalogStatus("unknown").IsValid() {
		t.Fatal("unknown catalog status should be invalid")
	}
}
