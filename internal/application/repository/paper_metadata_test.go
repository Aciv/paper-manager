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
