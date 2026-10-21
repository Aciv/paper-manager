package service

import (
	"context"
	"strings"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

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
