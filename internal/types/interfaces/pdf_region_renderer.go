package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// PDFRegionRenderer renders a stored PDF rectangle into a temporary image.
//
// Framework status: this boundary intentionally has no application-service,
// HTTP, frontend, dependency-injection, or Docker integration yet. A future
// caller will own authorization, cache persistence, and asset lifecycle.
type PDFRegionRenderer interface {
	Render(ctx context.Context, request types.PDFRegionRenderRequest) (*types.RenderedPDFRegion, error)
}
