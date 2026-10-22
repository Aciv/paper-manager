package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PDFPageRenderer renders one page or page region. Implementations must resolve
// the opaque storage key only inside the parser/upload boundary.
type PDFPageRenderer interface {
	RenderPage(ctx context.Context, request types.PDFPageRenderRequest) (*types.RenderedPDFPage, error)
}
