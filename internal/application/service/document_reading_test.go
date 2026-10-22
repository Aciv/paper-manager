package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type documentPageRendererStub struct {
	request types.PDFPageRenderRequest
	result  *types.RenderedPDFPage
	err     error
}

func (stub *documentPageRendererStub) RenderPage(
	_ context.Context, request types.PDFPageRenderRequest,
) (*types.RenderedPDFPage, error) {
	stub.request = request
	return stub.result, stub.err
}

func TestDocumentReadingServiceRendersPage(t *testing.T) {
	documentID := uuid.New()
	renderer := &documentPageRendererStub{result: &types.RenderedPDFPage{
		PNG:         []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		ContentType: "image/png", Width: 10, Height: 20,
	}}
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		StorageKey: "pdf/paper.pdf", MarkdownGeneration: 7,
	}}
	service := NewDocumentReadingServiceWithRenderer(repo, nil, nil, renderer)
	bbox := &types.NormalizedPDFRect{X0: 0.1, Y0: 0.2, X1: 0.8, Y1: 0.9}
	result, err := service.RenderPage(context.Background(), documentID.String(), 3, bbox, 144)
	if err != nil {
		t.Fatalf("RenderPage() error = %v", err)
	}
	if result.Width != 10 || result.Height != 20 {
		t.Fatalf("result = %#v", result)
	}
	if renderer.request.SourceStorageKey != "pdf/paper.pdf" ||
		renderer.request.PageNumber != 3 ||
		renderer.request.DPI != 144 ||
		renderer.request.FullPage ||
		renderer.request.RecognitionGeneration != 7 {
		t.Fatalf("renderer request = %#v", renderer.request)
	}
	if renderer.request.BBox == nil || renderer.request.BBox.X0 != 0.1 {
		t.Fatalf("renderer bbox = %#v", renderer.request.BBox)
	}
}

func TestDocumentReadingServiceRejectsOversizedRenderedPage(t *testing.T) {
	documentID := uuid.New()
	renderer := &documentPageRendererStub{result: &types.RenderedPDFPage{
		PNG:         []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		ContentType: "image/png", Width: types.MaxPDFRenderPixels + 1, Height: 1,
	}}
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, StorageKey: "pdf/paper.pdf",
	}}
	service := NewDocumentReadingServiceWithRenderer(repo, nil, nil, renderer)
	if _, err := service.RenderPage(context.Background(), documentID.String(), 1, nil, 144); !errors.Is(err, types.ErrPDFRenderTooLarge) {
		t.Fatalf("oversized page error = %v", err)
	}
}

func TestDocumentReadingServiceRejectsUnavailableRendererAndInvalidBBox(t *testing.T) {
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, StorageKey: "pdf/paper.pdf",
	}}
	service := NewDocumentReadingService(repo, nil, nil)
	if _, err := service.RenderPage(context.Background(), documentID.String(), 1, nil, 0); !errors.Is(err, types.ErrPDFRenderUnavailable) {
		t.Fatalf("unavailable renderer error = %v", err)
	}
	renderer := &documentPageRendererStub{}
	service = NewDocumentReadingServiceWithRenderer(repo, nil, nil, renderer)
	bbox := &types.NormalizedPDFRect{X0: 0.8, Y0: 0.2, X1: 0.1, Y1: 0.9}
	if _, err := service.RenderPage(context.Background(), documentID.String(), 1, bbox, 144); !errors.Is(err, types.ErrInvalidPDFRenderRequest) {
		t.Fatalf("invalid bbox error = %v", err)
	}
}

func TestDocumentReadingServiceReadsMarkdownAndBlocks(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatal(err)
	}
	storedMarkdown, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "parsed.md", ContentType: "text/markdown", Category: interfaces.StorageCategoryMarkdown,
		Content: strings.NewReader("# Paper\nbody"),
	})
	if err != nil {
		t.Fatal(err)
	}
	page := 1
	documentID, blockID := uuid.New(), uuid.New()
	repo := &documentStatusRepoStub{
		document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable, OriginalFilename: "paper.pdf"},
		markdown: types.DocumentMarkdown{
			DocumentID: documentID, StorageKey: storedMarkdown.Key, Filename: "parsed.md",
			ContentType: "text/markdown", SizeBytes: storedMarkdown.Size, PageCount: 2, RecognitionGeneration: 3,
		},
		blocks: []types.Block{{
			ID: blockID, DocumentID: documentID, Ordinal: 0, Type: "text", Text: "# Paper",
			Location: &types.PDFBlockLocation{PageNumber: &page},
		}},
	}
	service := NewDocumentReadingService(repo, storage, nil)

	content, err := service.GetDocumentMarkdown(context.Background(), documentID.String())
	if err != nil || string(content.Markdown) != "# Paper\nbody" ||
		content.PageCount != 2 || content.RecognitionGeneration != 3 || content.Filename != "parsed.md" {
		t.Fatalf("Markdown content = %#v, error=%v", content, err)
	}
	blocks, err := service.GetDocumentBlocks(context.Background(), documentID.String(), 1)
	if err != nil || len(blocks) != 1 || blocks[0].ID != blockID {
		t.Fatalf("blocks = %#v, error=%v", blocks, err)
	}
	if _, err := service.GetDocumentBlocks(context.Background(), documentID.String(), -1); err == nil {
		t.Fatal("negative page was accepted")
	}
}
