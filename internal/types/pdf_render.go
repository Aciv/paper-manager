package types

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var (
	ErrInvalidPDFRenderRequest = errors.New("invalid PDF render request")
	ErrPDFRenderUnavailable    = errors.New("PDF renderer is unavailable")
	ErrPDFRenderTooLarge       = errors.New("rendered PDF page exceeds the output limit")
)

const (
	DefaultPDFRenderDPI     = 144
	MaxPDFRenderDPI         = 300
	MaxPDFRenderPixels      = 20_000_000
	MaxPDFRenderOutputBytes = 12 << 20
	PDFRenderRequestTimeout = 20 * time.Second
)

// NormalizedPDFRect is a page rectangle in [0,1] coordinates. It is independent
// of page rotation; the renderer converts it against the page rectangle.
type NormalizedPDFRect struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

func (rect NormalizedPDFRect) Validate() error {
	values := []float64{rect.X0, rect.Y0, rect.X1, rect.Y1}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: bbox coordinates must be finite", ErrInvalidPDFRenderRequest)
		}
	}
	if rect.X0 < 0 || rect.Y0 < 0 || rect.X0 >= rect.X1 || rect.Y0 >= rect.Y1 ||
		rect.X1 > 1 || rect.Y1 > 1 {
		return fmt.Errorf("%w: bbox must be a positive normalized rectangle", ErrInvalidPDFRenderRequest)
	}
	return nil
}

// PDFPageRenderRequest is the application-to-renderer contract. SourceStorageKey
// is obtained from the document record and never returned to clients.
type PDFPageRenderRequest struct {
	DocumentID            string
	SourceStorageKey      string
	PageNumber            int
	FullPage              bool
	BBox                  *NormalizedPDFRect
	DPI                   int
	RecognitionGeneration int64
}

// RenderedPDFPage is a bounded PNG plus page/pixel metadata.
type RenderedPDFPage struct {
	PNG         []byte
	ContentType string
	Width       int
	Height      int
	PageWidth   float64
	PageHeight  float64
}
