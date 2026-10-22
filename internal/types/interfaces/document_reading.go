package interfaces

import (
	"context"
	"io"

	"paperMaster/internal/types"
)

// DocumentReadingService is the read-only boundary for one source document:
// original content, parsed Markdown, page-scoped blocks, block assets, and the
// disposable reading structure. It never mutates document lifecycle or files.
type DocumentReadingService interface {
	OpenDocumentFile(ctx context.Context, id string) (document *types.Document, reader io.ReadCloser, filename string, err error)
	GetDocumentBlocks(ctx context.Context, id string, pageNumber int) ([]types.Block, error)
	GetDocumentBlockImage(ctx context.Context, documentID, blockID string) (reader io.ReadCloser, filename string, err error)
	GetDocumentMarkdown(ctx context.Context, id string) (types.DocumentMarkdownContent, error)
	GetDocumentReadingStructure(ctx context.Context, id string) (types.DocumentReadingStructure, error)
	// RenderPage returns a bounded page/region PNG. The storage key remains
	// internal to the application and renderer.
	RenderPage(
		ctx context.Context, documentID string, pageNumber int,
		bbox *types.NormalizedPDFRect, dpi int,
	) (*types.RenderedPDFPage, error)
}
