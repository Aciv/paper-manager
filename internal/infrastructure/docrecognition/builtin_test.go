package docrecognition

import (
	"context"
	"testing"

	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"google.golang.org/grpc"
)

type builtinParserClientStub struct {
	received *papersv1.RecognitionRequest
	result   *papersv1.RecognitionResult
	err      error
}

func (s *builtinParserClientStub) Recognize(_ context.Context, request *papersv1.RecognitionRequest, _ ...grpc.CallOption) (*papersv1.RecognitionResult, error) {
	s.received = request
	return s.result, s.err
}

func TestBuiltinRecognizerMapsByteRequestAndResponse(t *testing.T) {
	markdown := []byte("# Parsed paper\n")
	client := &builtinParserClientStub{result: &papersv1.RecognitionResult{
		Markdown:         markdown,
		MarkdownFilename: "paper.md",
		ContentType:      "text/markdown",
		SizeBytes:        int64(len(markdown)),
		Sha256:           "abc123",
		Language:         "en",
		PageCount:        3,
		ParserName:       "pymupdf4llm",
		Blocks: []*papersv1.RecognitionBlock{{
			Ordinal: 0, Type: "title", Text: "Parsed paper", PageNumber: 1,
			PageWidth: 612, PageHeight: 792, BboxX0: 72, BboxY0: 72,
			BboxX1: 240, BboxY1: 108, MarkdownCodepointStart: 0, MarkdownCodepointEnd: 14,
		}},
	}}
	recognizer := NewBuiltinRecognizerWithClient(client)
	content := []byte("%PDF-1.7")
	documentID := uuid.New()
	result, err := recognizer.Read(context.Background(), &types.RecognitionRequest{
		DocumentID:       documentID,
		OriginalFilename: "paper.pdf",
		ContentType:      "application/pdf; charset=binary",
		SizeBytes:        int64(len(content)),
		InputMode:        types.RecognitionInputBytes,
		Content:          content,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if client.received.GetDocumentId() != documentID.String() || string(client.received.GetContent()) != string(content) {
		t.Fatalf("unexpected protobuf request: %+v", client.received)
	}
	if string(result.Markdown) != string(markdown) || result.Metadata.PageCount != 3 || result.Metadata.MarkdownFilename != "paper.md" {
		t.Fatalf("unexpected recognition result: %+v", result)
	}
	if len(result.Blocks) != 1 || result.Blocks[0].Location.PageNumber == nil || *result.Blocks[0].Location.PageNumber != 1 {
		t.Fatalf("unexpected recognition blocks: %+v", result.Blocks)
	}
}
