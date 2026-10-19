package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func referenceSourceText(value string) *string {
	return &value
}

func TestPaperServiceDeleteReferenceRemovesOwnedRow(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Ada Lovelace. 2024. A removable reference.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}

	if err := service.DeleteReference(context.Background(), citation.ReferenceID.String()); err != nil {
		t.Fatalf("DeleteReference() error = %v", err)
	}
	if len(citations.items) != 0 {
		t.Fatalf("remaining references = %#v", citations.items)
	}
	if err := service.DeleteReference(context.Background(), citation.ReferenceID.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second DeleteReference() error = %v, want sql.ErrNoRows", err)
	}
}

func TestPaperServiceUpdateReferenceSourceNormalizesAndFencesUnresolved(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Original reference text.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	stored := citations.items[citation.ReferenceID]
	stored.ResolutionStatus = types.DocumentStageQueued
	stored.ResolutionGeneration = 3
	stored.ResolutionProvider = "crossref"
	stored.ResolutionErrorCode = referenceSourceText("old_code")
	stored.ResolutionErrorMsg = referenceSourceText("old error")
	citations.items[citation.ReferenceID] = stored

	updated, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), types.PaperReferenceSourceUpdate{
		DOI:         referenceSourceText("https://doi.org/10.1000/ABC."),
		Title:       referenceSourceText("  Corrected title  "),
		Authors:     &[]types.PaperAuthor{{Name: " Ada Lovelace ", Affiliation: " Analytical Engine Lab "}},
		Venue:       referenceSourceText(" NeurIPS "),
		PublishedAt: referenceSourceText("2024-03-02"),
		Volume:      referenceSourceText("12"),
		Issue:       referenceSourceText("3"),
		Pages:       referenceSourceText("1-10"),
		SourceURL:   referenceSourceText(" https://example.org/paper.pdf "),
	})
	if err != nil {
		t.Fatalf("UpdateReferenceSource() error = %v", err)
	}
	if updated.DOI != "10.1000/abc" ||
		updated.Title != "Corrected title" ||
		updated.Venue != "NeurIPS" ||
		updated.Volume != "12" ||
		updated.Issue != "3" ||
		updated.Pages != "1-10" ||
		updated.SourceURL != "https://example.org/paper.pdf" {
		t.Fatalf("updated source = %#v", updated)
	}
	if len(updated.Authors) != 1 ||
		updated.Authors[0].Name != "Ada Lovelace" ||
		updated.Authors[0].Affiliation != "Analytical Engine Lab" {
		t.Fatalf("updated authors = %#v", updated.Authors)
	}
	if updated.PublishedAt == nil || !updated.PublishedAt.Equal(time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("publishedAt = %v", updated.PublishedAt)
	}
	if updated.MetadataSource != types.PaperMetadataSourceManual {
		t.Fatalf("metadata source = %q, want manual", updated.MetadataSource)
	}
	if citations.items[citation.ReferenceID].EffectiveOrigin() != types.PaperCitationOriginManual {
		t.Fatalf(
			"origin = %q, want manual",
			citations.items[citation.ReferenceID].Origin,
		)
	}
	if updated.ResolutionGeneration != 4 ||
		updated.ResolutionStatus != types.DocumentStageNotStarted ||
		updated.ResolutionProvider != "" ||
		updated.ResolutionErrorCode != nil ||
		updated.ResolutionErrorMsg != nil {
		t.Fatalf("resolution fence = %#v", updated)
	}

	cleared, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), types.PaperReferenceSourceUpdate{
		Title:       referenceSourceText(""),
		Authors:     &[]types.PaperAuthor{},
		PublishedAt: referenceSourceText(""),
		SourceURL:   referenceSourceText(""),
	})
	if err != nil {
		t.Fatalf("clear source fields: %v", err)
	}
	if cleared.Title != "" || len(cleared.Authors) != 0 || cleared.PublishedAt != nil || cleared.SourceURL != "" {
		t.Fatalf("cleared source = %#v", cleared)
	}
}

func TestPaperServiceUpdateReferenceSourceKeepsLinkedResolution(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, citations := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Linked reference text.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	targetID := uuid.New()
	stored := citations.items[citation.ReferenceID]
	stored.CitedDocumentID = &targetID
	stored.ResolutionStatus = types.DocumentStageSucceeded
	stored.ResolutionGeneration = 7
	citations.items[citation.ReferenceID] = stored

	updated, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), types.PaperReferenceSourceUpdate{
		Title: referenceSourceText("Corrected linked title"),
	})
	if err != nil {
		t.Fatalf("UpdateReferenceSource() error = %v", err)
	}
	if updated.Title != "Corrected linked title" ||
		updated.CitedDocumentID == nil || *updated.CitedDocumentID != targetID ||
		updated.ResolutionStatus != types.DocumentStageSucceeded ||
		updated.ResolutionGeneration != 7 {
		t.Fatalf("linked edit = %#v", updated)
	}
}

func TestPaperServiceUpdateReferenceSourceRejectsInvalidInput(t *testing.T) {
	citing := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service, _, _, _ := newPaperServiceForTest(citing)
	citation, err := service.AddCitation(context.Background(), citing.ID.String(), types.PaperCitationInput{
		RawReference: "Validated reference text.",
	})
	if err != nil {
		t.Fatalf("AddCitation() error = %v", err)
	}
	tooManyAuthors := make([]types.PaperAuthor, types.MaxPaperReferenceAuthors+1)
	for index := range tooManyAuthors {
		tooManyAuthors[index] = types.PaperAuthor{Name: "Author"}
	}
	cases := []struct {
		name   string
		update types.PaperReferenceSourceUpdate
	}{
		{name: "empty update", update: types.PaperReferenceSourceUpdate{}},
		{name: "invalid date", update: types.PaperReferenceSourceUpdate{PublishedAt: referenceSourceText("2024-02-31")}},
		{name: "non-http source url", update: types.PaperReferenceSourceUpdate{SourceURL: referenceSourceText("javascript:alert(1)")}},
		{name: "empty author name", update: types.PaperReferenceSourceUpdate{Authors: &[]types.PaperAuthor{{Name: "  "}}}},
		{name: "too many authors", update: types.PaperReferenceSourceUpdate{Authors: &tooManyAuthors}},
		{name: "control character", update: types.PaperReferenceSourceUpdate{Title: referenceSourceText("bad\ntitle")}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.UpdateReferenceSource(context.Background(), citation.ReferenceID.String(), testCase.update)
			if !errors.Is(err, types.ErrInvalidPaperReference) {
				t.Fatalf("error = %v, want ErrInvalidPaperReference", err)
			}
		})
	}
}
