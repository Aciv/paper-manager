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

func TestPaperMetadataWritableSourcesEnforcesPriority(t *testing.T) {
	tests := []struct {
		incoming types.PaperMetadataSource
		want     []types.PaperMetadataSource
	}{
		{
			incoming: types.PaperMetadataSourceExtraction,
			want: []types.PaperMetadataSource{
				types.PaperMetadataSourceUnknown,
				types.PaperMetadataSourceExtraction,
			},
		},
		{
			incoming: types.PaperMetadataSourceCatalog,
			want: []types.PaperMetadataSource{
				types.PaperMetadataSourceUnknown,
				types.PaperMetadataSourceExtraction,
				types.PaperMetadataSourceCatalog,
			},
		},
		{
			incoming: types.PaperMetadataSourceManual,
			want: []types.PaperMetadataSource{
				types.PaperMetadataSourceUnknown,
				types.PaperMetadataSourceExtraction,
				types.PaperMetadataSourceCatalog,
				types.PaperMetadataSourceManual,
			},
		},
	}
	for _, test := range tests {
		got := paperMetadataWritableSources(test.incoming)
		if len(got) != len(test.want) {
			t.Fatalf("%s writable source count = %d, want %d",
				test.incoming, len(got), len(test.want))
		}
		for index := range test.want {
			if got[index] != test.want[index] {
				t.Fatalf("%s writable sources = %#v, want %#v",
					test.incoming, got, test.want)
			}
		}
	}
}
