package service

import (
	"reflect"
	"testing"

	"paperMaster/internal/types"
)

func TestPaperCatalogAuthorNamesDropsLayoutNoise(t *testing.T) {
	got := paperCatalogAuthorNames([]types.PaperAuthor{
		{Name: "Zhen Li∗†"},
		{Name: "Huazhong University of Science and"},
		{Name: "3"},
		{Name: `<div class="left">`},
		{Name: "Yao Wan"},
		{Name: "Yao Wan"},
	})
	want := []string{"Zhen Li", "Yao Wan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paperCatalogAuthorNames() = %#v, want %#v", got, want)
	}
}

func TestExtractionMetadataUpdateSanitizesAuthors(t *testing.T) {
	update := extractionMetadataUpdate(&types.PaperExtractionResult{
		Authors: []types.PaperTextCandidate{
			{Value: "Alice Zhang"},
			{Value: "3"},
			{Value: "<ul>"},
			{Value: "Tsinghua University"},
			{Value: "alice zhang"},
		},
	})
	if update.Authors == nil {
		t.Fatal("expected sanitized authors")
	}
	got := *update.Authors
	if len(got) != 1 || got[0].Name != "Alice Zhang" {
		t.Fatalf("authors = %#v, want [Alice Zhang]", got)
	}
}
