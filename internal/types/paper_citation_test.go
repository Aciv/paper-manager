package types

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPaperCitationValidate(t *testing.T) {
	citingID := uuid.New()
	citedID := uuid.New()

	tests := []struct {
		name     string
		citation PaperCitation
		wantErr  error
	}{
		{
			name: "valid directional relation",
			citation: PaperCitation{
				CitingDocumentID: citingID,
				CitedDocumentID:  citedID,
				RawReference:     "Example Author. Example Paper.",
			},
		},
		{
			name: "missing endpoint",
			citation: PaperCitation{
				CitingDocumentID: citingID,
				RawReference:     "Example Author. Example Paper.",
			},
			wantErr: ErrInvalidPaperCitation,
		},
		{
			name: "self citation",
			citation: PaperCitation{
				CitingDocumentID: citingID,
				CitedDocumentID:  citingID,
				RawReference:     "Example Author. Example Paper.",
			},
			wantErr: ErrSelfPaperCitation,
		},
		{
			name: "missing raw reference",
			citation: PaperCitation{
				CitingDocumentID: citingID,
				CitedDocumentID:  citedID,
			},
			wantErr: ErrInvalidPaperCitation,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.citation.Validate()
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestPaperCitationTableName(t *testing.T) {
	if got := (PaperCitation{}).TableName(); got != "paper_citations" {
		t.Fatalf("TableName() = %q, want paper_citations", got)
	}
}

func TestPaperCitationValidateEndpointsDoesNotRequireRawReference(t *testing.T) {
	citation := PaperCitation{
		CitingDocumentID: uuid.New(),
		CitedDocumentID:  uuid.New(),
	}
	if err := citation.ValidateEndpoints(); err != nil {
		t.Fatalf("ValidateEndpoints() error = %v", err)
	}
}
