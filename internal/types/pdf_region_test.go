package types

import (
	"errors"
	"math"
	"testing"
)

func TestPDFRegionRenderRequestValidate(t *testing.T) {
	valid := PDFRegionRenderRequest{
		SourceStorageKey: "pdf/source.pdf", PageNumber: 2,
		BBoxX0: 10, BBoxY0: 20, BBoxX1: 210, BBoxY1: 120, DPI: 216,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	tests := []PDFRegionRenderRequest{
		{},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 0, BBoxX1: 1, BBoxY1: 1, DPI: 72},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 1, BBoxX0: 2, BBoxX1: 1, BBoxY1: 1, DPI: 72},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 1, BBoxX1: math.NaN(), BBoxY1: 1, DPI: 72},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 1, BBoxX1: 1, BBoxY1: 1, DPI: 0},
	}
	for index, request := range tests {
		if err := request.Validate(); !errors.Is(err, ErrInvalidPDFRegionRender) {
			t.Fatalf("case %d error = %v, want ErrInvalidPDFRegionRender", index, err)
		}
	}
}
