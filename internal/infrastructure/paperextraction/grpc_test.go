package paperextraction

import (
	"context"
	"errors"
	"testing"

	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type extractorClientStub struct {
	received *papersv1.PaperExtractionRequest
	result   *papersv1.PaperExtractionResult
	err      error
	calls    int
}

func (stub *extractorClientStub) Extract(
	_ context.Context,
	request *papersv1.PaperExtractionRequest,
	_ ...grpc.CallOption,
) (*papersv1.PaperExtractionResult, error) {
	stub.calls++
	stub.received = request
	return stub.result, stub.err
}

func TestGRPCExtractorMapsRequestResponseAndEvidence(t *testing.T) {
	markdown := []byte("# **Mapped Paper**\n\n### **Ada Lovelace**\n")
	location := completeTestLocation(len([]rune(string(markdown))))
	client := &extractorClientStub{
		result: &papersv1.PaperExtractionResult{
			Title: &papersv1.PaperTextCandidate{
				Value:      "Mapped Paper",
				Confidence: 0.95,
				Evidence: []*papersv1.PaperSourceEvidence{{
					BlockOrdinal:           0,
					PageNumber:             1,
					MarkdownCodepointStart: 0,
					MarkdownCodepointEnd:   int32(len([]rune(string(markdown)))),
				}},
			},
			Authors: []*papersv1.PaperTextCandidate{{
				Value:      "Ada Lovelace",
				Confidence: 0.85,
			}},
			References:       []string{"[1] A raw reference."},
			ExtractorName:    "markdown-rules",
			ExtractorVersion: "1",
		},
	}
	extractor := NewGRPCExtractorWithClient(client)
	documentID := uuid.New()

	result, err := extractor.Extract(context.Background(), &types.PaperExtractionRequest{
		DocumentID: documentID,
		Markdown:   markdown,
		Blocks: []types.PaperSourceBlock{{
			Ordinal:  0,
			Type:     "title",
			Text:     "Mapped Paper",
			Location: location,
		}},
	})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if client.received.GetDocumentId() != documentID.String() ||
		string(client.received.GetMarkdown()) != string(markdown) ||
		client.received.GetBlocks()[0].GetLocation().GetPageNumber() != 1 {
		t.Fatalf("unexpected protobuf request: %+v", client.received)
	}
	if result.Title == nil ||
		result.Title.Value != "Mapped Paper" ||
		len(result.Title.Evidence) != 1 ||
		len(result.Authors) != 1 ||
		len(result.References) != 1 {
		t.Fatalf("unexpected extraction result: %+v", result)
	}
}

func TestGRPCExtractorRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := &extractorClientStub{}
	extractor := NewGRPCExtractorWithClient(client)

	_, err := extractor.Extract(context.Background(), &types.PaperExtractionRequest{
		DocumentID: uuid.New(),
	})
	if !errors.Is(err, types.ErrInvalidPaperExtraction) {
		t.Fatalf("expected invalid extraction error, got %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("RPC called %d times for invalid input", client.calls)
	}
}

func TestGRPCExtractorRejectsInvalidResponse(t *testing.T) {
	markdown := []byte("# Paper")
	client := &extractorClientStub{result: &papersv1.PaperExtractionResult{
		Title: &papersv1.PaperTextCandidate{
			Value:      "Paper",
			Confidence: 1.5,
		},
		ExtractorName: "markdown-rules",
	}}
	extractor := NewGRPCExtractorWithClient(client)

	_, err := extractor.Extract(context.Background(), &types.PaperExtractionRequest{
		DocumentID: uuid.New(),
		Markdown:   markdown,
	})
	if !errors.Is(err, types.ErrInvalidPaperExtraction) {
		t.Fatalf("expected invalid extraction response error, got %v", err)
	}
}

func completeTestLocation(markdownEnd int) *types.PDFBlockLocation {
	pageNumber := 1
	pageWidth, pageHeight := 612.0, 792.0
	x0, y0, x1, y1 := 72.0, 72.0, 300.0, 110.0
	start := 0
	return &types.PDFBlockLocation{
		PageNumber:             &pageNumber,
		PageWidth:              &pageWidth,
		PageHeight:             &pageHeight,
		BBoxX0:                 &x0,
		BBoxY0:                 &y0,
		BBoxX1:                 &x1,
		BBoxY1:                 &y1,
		MarkdownCodepointStart: &start,
		MarkdownCodepointEnd:   &markdownEnd,
	}
}
