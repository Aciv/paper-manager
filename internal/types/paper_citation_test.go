package types

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func testCitation(citingID, referenceID uuid.UUID, raw string) PaperCitation {
	return PaperCitation{
		PaperReference: PaperReference{
			ReferenceID:      referenceID,
			CitingDocumentID: citingID,
			RawReference:     raw,
			Authors:          []PaperAuthor{},
			ResolutionStatus: DocumentStageNotStarted,
			MetadataSource:   PaperMetadataSourceUnknown,
		},
	}
}

func TestPaperCitationValidate(t *testing.T) {
	citingID := uuid.New()
	citedID := uuid.New()
	referenceID := uuid.New()
	valid := testCitation(citingID, referenceID, "Example Author. Example Paper.")

	tests := []struct {
		name     string
		citation PaperCitation
		wantErr  error
	}{
		{name: "valid directional relation", citation: func() PaperCitation {
			citation := valid
			citation.CitedDocumentID = &citedID
			citation.ResolutionStatus = DocumentStageSucceeded
			return citation
		}()},
		{name: "unresolved reference is valid", citation: valid},
		{name: "missing reference", citation: func() PaperCitation {
			citation := valid
			citation.ReferenceID = uuid.Nil
			return citation
		}(), wantErr: ErrInvalidPaperCitation},
		{name: "self citation", citation: func() PaperCitation {
			citation := valid
			citation.CitedDocumentID = &citingID
			return citation
		}(), wantErr: ErrSelfPaperCitation},
		{name: "missing raw reference", citation: func() PaperCitation {
			citation := valid
			citation.RawReference = ""
			return citation
		}(), wantErr: ErrInvalidPaperReference},
		{name: "negative bibliography order", citation: func() PaperCitation {
			citation := valid
			order := -1
			citation.BibliographyOrder = &order
			return citation
		}(), wantErr: ErrInvalidPaperReference},
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
	citedID := uuid.New()
	citation := testCitation(uuid.New(), uuid.New(), "")
	citation.CitedDocumentID = &citedID
	if err := citation.ValidateEndpoints(); err != nil {
		t.Fatalf("ValidateEndpoints() error = %v", err)
	}
}

func TestPaperReferenceIdentityIsScopedToCitingPaper(t *testing.T) {
	citingID := uuid.New()
	otherCitingID := uuid.New()
	raw := "  Example   Author. Example Paper.  "
	first := PaperReferenceIDForRaw(citingID, raw)
	second := PaperReferenceIDForRaw(citingID, "Example Author. Example Paper.")
	if first != second {
		t.Fatalf("normalized raw reference identity changed: %s != %s", first, second)
	}
	if PaperReferenceIDForRaw(otherCitingID, raw) == first {
		t.Fatal("the same raw reference in another paper must have a distinct row identity")
	}
}
