package docrecognition

import (
	"context"
	"errors"
	"strings"
	"testing"

	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type builtinParserClientStub struct {
	received *papersv1.RecognitionRequest
	result   *papersv1.RecognitionResult
	err      error
}

func TestBuiltinRecognizerMapsStorageKeyAndSizeWithoutContent(t *testing.T) {
	client := &builtinParserClientStub{result: &papersv1.RecognitionResult{}}
	recognizer := NewBuiltinRecognizerWithClient(client)
	_, err := recognizer.Read(context.Background(), &types.RecognitionRequest{
		DocumentID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: "application/pdf",
		InputMode: types.RecognitionInputStorageKey, SourceStorageKey: "pdf/shared.pdf", SizeBytes: 123,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !recognizer.SupportsStorageKey() || client.received.GetSourceStorageKey() != "pdf/shared.pdf" || client.received.SourceSizeBytes == nil || client.received.GetSourceSizeBytes() != 123 || client.received.GetContent() != nil {
		t.Fatalf("request=%+v", client.received)
	}
}

func TestBuiltinRecognizerClassifiesOnlyUnavailableStoredInputsForFallback(t *testing.T) {
	for _, test := range []struct {
		name     string
		code     codes.Code
		message  string
		fallback bool
	}{
		{"missing", codes.NotFound, "stored recognition source is unavailable", true},
		{"unsupported", codes.Unimplemented, "key unsupported", true},
		{"legacy stub", codes.InvalidArgument, "exactly one recognition source is required", true},
		{"invalid key", codes.InvalidArgument, "source storage key must be a relative path", false},
		{"outside root", codes.PermissionDenied, "outside root", false},
		{"size", codes.ResourceExhausted, "too large", false},
		{"parse error", codes.Internal, "invalid PDF", false},
		{"offline", codes.Unavailable, "offline", false},
		{"cancel", codes.Canceled, "cancelled", false},
		{"deadline", codes.DeadlineExceeded, "deadline", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := NewBuiltinRecognizerWithClient(&builtinParserClientStub{err: status.Error(test.code, test.message)})
			request := &types.RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: "application/pdf", SizeBytes: 4, InputMode: types.RecognitionInputStorageKey, SourceStorageKey: "pdf/source.pdf"}
			_, err := r.Read(context.Background(), request)
			if errors.Is(err, types.ErrRecognitionStoredSourceUnavailable) != test.fallback {
				t.Fatalf("key error=%v fallback=%t", err, test.fallback)
			}
			request.InputMode, request.SourceStorageKey, request.Content = types.RecognitionInputBytes, "", []byte("%PDF")
			_, err = r.Read(context.Background(), request)
			if errors.Is(err, types.ErrRecognitionStoredSourceUnavailable) {
				t.Fatalf("bytes call must not request fallback: %v", err)
			}
		})
	}
}

func TestBuiltinRecognizerClassifiesUnavailableService(t *testing.T) {
	recognizer := NewBuiltinRecognizerWithClient(&builtinParserClientStub{
		err: status.Error(codes.Unavailable, "connection refused"),
	})
	_, err := recognizer.Read(context.Background(), &types.RecognitionRequest{
		DocumentID:       uuid.New(),
		OriginalFilename: "paper.pdf",
		ContentType:      "application/pdf",
		SizeBytes:        4,
		InputMode:        types.RecognitionInputBytes,
		Content:          []byte("%PDF"),
	})
	if !errors.Is(err, types.ErrRecognitionServiceUnavailable) {
		t.Fatalf("Read() error = %v, want ErrRecognitionServiceUnavailable", err)
	}
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
		Language:         "en",
		PageCount:        3,
		ParserName:       "pymupdf4llm",
		Blocks: []*papersv1.RecognitionBlock{{
			Ordinal: 0, Type: "title", Text: "Parsed paper", PageNumber: 1,
			PageWidth: 612, PageHeight: 792, BboxX0: 72, BboxY0: 72,
			BboxX1: 240, BboxY1: 108, MarkdownCodepointStart: 0, MarkdownCodepointEnd: 14,
		}, {
			Ordinal: 1, Type: "image", Text: "diagram", PageNumber: 1,
			PageWidth: 612, PageHeight: 792, BboxX0: 72, BboxY0: 120,
			BboxX1: 240, BboxY1: 280, MarkdownCodepointStart: 14, MarkdownCodepointEnd: 14,
			ImageData: []byte("png"), ImageContentType: "image/png",
		}},
		Outline: &papersv1.RecognitionOutline{
			SchemaVersion: 1, Source: "none", Diagnostics: []string{"outline_unavailable"},
		},
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
	if client.received.GetContentType() != "application/pdf" {
		t.Fatalf("content type was not normalized: %q", client.received.GetContentType())
	}
	if string(result.Markdown) != string(markdown) || result.Metadata.PageCount != 3 || result.Metadata.MarkdownFilename != "paper.md" {
		t.Fatalf("unexpected recognition result: %+v", result)
	}
	if len(result.Blocks) != 2 || result.Blocks[0].Location.PageNumber == nil || *result.Blocks[0].Location.PageNumber != 1 || string(result.Blocks[1].ImageData) != "png" {
		t.Fatalf("unexpected recognition blocks: %+v", result.Blocks)
	}
}

func TestBuiltinRecognizerRejectsInvalidOutlineProjection(t *testing.T) {
	markdown := []byte("# Paper\n")
	recognizer := NewBuiltinRecognizerWithClient(&builtinParserClientStub{result: &papersv1.RecognitionResult{
		Markdown: markdown, SizeBytes: int64(len(markdown)), ContentType: "text/markdown", PageCount: 1,
		Outline: &papersv1.RecognitionOutline{SchemaVersion: 1, Source: "pdf_outline",
			Sections: []*papersv1.RecognitionOutlineSection{{Ordinal: 0, Title: "Paper", Level: 2, ParentOrdinal: 0,
				StartPage: 1, EndPage: 1, StartBlockOrdinal: -1, EndBlockOrdinal: -1,
				MarkdownCodepointStart: -1, MarkdownCodepointEnd: -1}}},
	}})
	content := []byte("%PDF")
	_, err := recognizer.Read(context.Background(), &types.RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: int64(len(content)), InputMode: types.RecognitionInputBytes, Content: content})
	if err == nil || !strings.Contains(err.Error(), "invalid outline") {
		t.Fatalf("Read() error = %v, want invalid outline", err)
	}
}

func TestBuiltinRecognizerAcceptsOlderParserWithoutOutlineAsExplicitNone(t *testing.T) {
	markdown := []byte("paper")
	recognizer := NewBuiltinRecognizerWithClient(&builtinParserClientStub{result: &papersv1.RecognitionResult{
		Markdown: markdown, SizeBytes: int64(len(markdown)), ContentType: "text/markdown", PageCount: 1,
	}})
	content := []byte("%PDF")
	result, err := recognizer.Read(context.Background(), &types.RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: int64(len(content)), InputMode: types.RecognitionInputBytes, Content: content})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if result.Outline.Source != types.DocumentOutlineNone || len(result.Outline.Diagnostics) != 1 {
		t.Fatalf("outline = %#v", result.Outline)
	}
}
