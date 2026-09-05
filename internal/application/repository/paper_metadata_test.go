package repository

import (
	"testing"

	"paperMaster/internal/types"

	"gorm.io/gorm/clause"
)

func TestPaperMetadataChangesEncodesEmptyTagsAsJSONBArray(t *testing.T) {
	tags := []string{}
	changes, err := paperMetadataChanges(&types.PaperMetadataUpdate{Tags: &tags})
	if err != nil {
		t.Fatalf("paperMetadataChanges() error = %v", err)
	}
	expression, ok := changes["tags"].(clause.Expr)
	if !ok {
		t.Fatalf("tags change type = %T, want clause.Expr", changes["tags"])
	}
	if expression.SQL != "?::jsonb" || len(expression.Vars) != 1 || expression.Vars[0] != "[]" {
		t.Fatalf("tags expression = %#v, want JSONB empty array", expression)
	}
}

func TestPaperMetadataChangesNormalizesNilTagsToJSONBArray(t *testing.T) {
	var tags []string
	changes, err := paperMetadataChanges(&types.PaperMetadataUpdate{Tags: &tags})
	if err != nil {
		t.Fatalf("paperMetadataChanges() error = %v", err)
	}
	expression := changes["tags"].(clause.Expr)
	if expression.Vars[0] != "[]" {
		t.Fatalf("nil tags encoded as %#v, want []", expression.Vars[0])
	}
}

func TestMergePaperMetadataChangesKeepsManualFieldsForAutomaticUpdates(t *testing.T) {
	automaticTitle := "parser title"
	automaticAbstract := "parser abstract"
	existing := types.PaperMetadataFieldSources{
		"title": types.PaperMetadataSourceManual,
	}
	source := types.PaperMetadataSourceExtraction
	changes, sources, applied, err := mergePaperMetadataChanges(existing, &types.PaperMetadataUpdate{
		Title:          &automaticTitle,
		Abstract:       &automaticAbstract,
		MetadataSource: &source,
	})
	if err != nil {
		t.Fatalf("mergePaperMetadataChanges() error = %v", err)
	}
	if _, exists := changes["title"]; exists {
		t.Fatalf("automatic patch must not replace manual title: %#v", changes)
	}
	if got := changes["abstract"]; got != automaticAbstract {
		t.Fatalf("automatic abstract = %#v, want %q", got, automaticAbstract)
	}
	if got := sources["title"]; got != types.PaperMetadataSourceManual {
		t.Fatalf("title source = %q, want manual", got)
	}
	if got := sources["abstract"]; got != types.PaperMetadataSourceExtraction {
		t.Fatalf("abstract source = %q, want extraction", got)
	}
	if len(applied) != 1 || applied[0] != "abstract" {
		t.Fatalf("applied fields = %#v, want only abstract", applied)
	}
}

func TestMergePaperMetadataChangesAllowsExplicitManualClear(t *testing.T) {
	cleared := ""
	source := types.PaperMetadataSourceManual
	changes, sources, applied, err := mergePaperMetadataChanges(
		types.PaperMetadataFieldSources{"title": types.PaperMetadataSourceManual},
		&types.PaperMetadataUpdate{Title: &cleared, MetadataSource: &source},
	)
	if err != nil {
		t.Fatalf("mergePaperMetadataChanges() error = %v", err)
	}
	if got := changes["title"]; got != "" {
		t.Fatalf("manual title clear = %#v, want empty string", got)
	}
	if got := sources["title"]; got != types.PaperMetadataSourceManual {
		t.Fatalf("cleared title source = %q, want manual", got)
	}
	if len(applied) != 1 || applied[0] != "title" {
		t.Fatalf("applied fields = %#v, want title", applied)
	}
}
