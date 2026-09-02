package types

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	DocumentSelectionText  = "text"
	DocumentSelectionBlock = "block"
	DocumentSelectionArea  = "area"

	MaxDocumentSelectionTextRunes = 16_384
	MaxDocumentSelectionPage      = 100_000
	MaxDocumentSelectionImageSize = 4 << 20
	MaxDocumentSelectionDimension = 4_096
	MaxDocumentSelectionPixels    = 16_000_000
)

var ErrInvalidDocumentSelection = errors.New("invalid document selection")

// NormalizedDocumentRegion is a top-left/bottom-right rectangle expressed in
// page-relative coordinates. Every value is finite and within [0,1].
type NormalizedDocumentRegion struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// DocumentSelectionInput is the validated, request-scoped input handed to Go
// processing. ImageBytes is populated only for area selections and must never
// be persisted or serialized into an asynchronous task.
type DocumentSelectionInput struct {
	DocumentID  uuid.UUID
	Kind        string
	Text        string
	BlockID     uuid.UUID
	PageNumber  int
	Region      *NormalizedDocumentRegion
	ImageMIME   string
	ImageBytes  []byte
	ImageWidth  int
	ImageHeight int
}

// DocumentSelectionResult acknowledges what the transient processing boundary
// accepted. Real selection processing is intentionally deferred.
type DocumentSelectionResult struct {
	Kind        string `json:"kind"`
	PageNumber  int    `json:"pageNumber,omitempty"`
	BlockID     string `json:"blockId,omitempty"`
	TextRunes   int    `json:"textRunes,omitempty"`
	ImageWidth  int    `json:"imageWidth,omitempty"`
	ImageHeight int    `json:"imageHeight,omitempty"`
	Message     string `json:"message"`
}

// ValidateDocumentSelection validates invariants that remain important even
// when an input is constructed outside the HTTP handler.
func ValidateDocumentSelection(ctx context.Context, input DocumentSelectionInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document id is required", ErrInvalidDocumentSelection)
	}
	switch input.Kind {
	case DocumentSelectionText:
		if strings.TrimSpace(input.Text) == "" || !utf8.ValidString(input.Text) ||
			utf8.RuneCountInString(input.Text) > MaxDocumentSelectionTextRunes {
			return fmt.Errorf("%w: selected text is required", ErrInvalidDocumentSelection)
		}
		if input.PageNumber < 0 || input.PageNumber > MaxDocumentSelectionPage ||
			(input.Region != nil && !input.Region.Valid()) {
			return fmt.Errorf("%w: text source location is invalid", ErrInvalidDocumentSelection)
		}
	case DocumentSelectionBlock:
		if input.BlockID == uuid.Nil {
			return fmt.Errorf("%w: block id is required", ErrInvalidDocumentSelection)
		}
	case DocumentSelectionArea:
		if input.PageNumber < 1 || input.PageNumber > MaxDocumentSelectionPage {
			return fmt.Errorf("%w: page number is out of range", ErrInvalidDocumentSelection)
		}
		if input.Region == nil || !input.Region.Valid() {
			return fmt.Errorf("%w: normalized region is invalid", ErrInvalidDocumentSelection)
		}
		if input.ImageMIME != "image/png" || len(input.ImageBytes) == 0 ||
			len(input.ImageBytes) > MaxDocumentSelectionImageSize {
			return fmt.Errorf("%w: PNG capture is invalid", ErrInvalidDocumentSelection)
		}
		if input.ImageWidth < 1 || input.ImageHeight < 1 ||
			input.ImageWidth > MaxDocumentSelectionDimension ||
			input.ImageHeight > MaxDocumentSelectionDimension ||
			input.ImageWidth*input.ImageHeight > MaxDocumentSelectionPixels {
			return fmt.Errorf("%w: PNG dimensions are invalid", ErrInvalidDocumentSelection)
		}
	default:
		return fmt.Errorf("%w: unsupported selection kind", ErrInvalidDocumentSelection)
	}
	return nil
}

// Valid reports whether the rectangle is finite, normalized, and non-empty.
func (r NormalizedDocumentRegion) Valid() bool {
	values := []float64{r.X0, r.Y0, r.X1, r.Y1}
	for _, value := range values {
		if value != value || value < 0 || value > 1 {
			return false
		}
	}
	return r.X0 < r.X1 && r.Y0 < r.Y1
}
