package types

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

var ErrInvalidPDFRegionRender = errors.New("invalid PDF region render request")

// PDFRegionRenderRequest identifies one bounded rectangle in a stored PDF.
// DPI is a target ceiling: an implementation may reduce the effective scale
// to remain within its configured pixel budget.
type PDFRegionRenderRequest struct {
	SourceStorageKey string
	PageNumber       int
	BBoxX0           float64
	BBoxY0           float64
	BBoxX1           float64
	BBoxY1           float64
	DPI              int
}

func (request PDFRegionRenderRequest) Validate() error {
	if strings.TrimSpace(request.SourceStorageKey) == "" {
		return fmt.Errorf("%w: source storage key is required", ErrInvalidPDFRegionRender)
	}
	if request.PageNumber < 1 || request.PageNumber > 100_000 {
		return fmt.Errorf("%w: page number must be between 1 and 100000", ErrInvalidPDFRegionRender)
	}
	coordinates := []float64{request.BBoxX0, request.BBoxY0, request.BBoxX1, request.BBoxY1}
	for _, coordinate := range coordinates {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return fmt.Errorf("%w: bbox coordinates must be finite", ErrInvalidPDFRegionRender)
		}
	}
	if request.BBoxX0 < 0 || request.BBoxY0 < 0 ||
		request.BBoxX1 <= request.BBoxX0 || request.BBoxY1 <= request.BBoxY0 {
		return fmt.Errorf("%w: bbox must be a positive rectangle", ErrInvalidPDFRegionRender)
	}
	if request.DPI <= 0 {
		return fmt.Errorf("%w: DPI must be positive", ErrInvalidPDFRegionRender)
	}
	return nil
}

// RenderedPDFRegion is a temporary, validated PNG produced by a renderer.
// The caller must close Content; subprocess-backed implementations may remove
// their temporary artifact only when it is closed.
type RenderedPDFRegion struct {
	Content     io.ReadSeekCloser
	ContentType string
	Filename    string
	SizeBytes   int64
	Width       int
	Height      int
}
