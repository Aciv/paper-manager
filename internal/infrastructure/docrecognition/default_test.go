package docrecognition

import (
	"context"
	"errors"
	"strings"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDefaultRecognizerConvertsSupportedTextFormats(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		content     string
		contains    string
	}{
		{name: "plain text", filename: "notes.txt", contentType: "text/plain", content: "plain notes", contains: "plain notes"},
		{name: "markdown", filename: "notes.md", contentType: "text/markdown", content: "# Notes", contains: "# Notes"},
		{name: "json", filename: "paper.json", contentType: "application/json", content: `{"title":"Paper"}`, contains: "```json"},
		{name: "csv", filename: "papers.csv", contentType: "text/csv", content: "title,year\nPaper,2026\n", contains: "| title | year |"},
	}

	recognizer := NewDefaultRecognizer()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &types.RecognitionRequest{
				DocumentID:       uuid.New(),
				OriginalFilename: test.filename,
				ContentType:      test.contentType,
				SizeBytes:        int64(len(test.content)),
				InputMode:        types.RecognitionInputBytes,
				Content:          []byte(test.content),
			}
			result, err := recognizer.Read(context.Background(), request)
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if result.OutputMode != types.RecognitionOutputMarkdownBytes || result.Metadata.ContentType != "text/markdown" {
				t.Fatalf("unexpected result metadata: %+v", result.Metadata)
			}
			if !strings.Contains(string(result.Markdown), test.contains) {
				t.Fatalf("Markdown = %q, want substring %q", result.Markdown, test.contains)
			}
			if result.Metadata.MarkdownFilename != strings.TrimSuffix(test.filename, test.filename[strings.LastIndex(test.filename, "."):])+".md" {
				t.Fatalf("MarkdownFilename = %q", result.Metadata.MarkdownFilename)
			}
		})
	}
}

func TestDefaultRecognizerRejectsNonByteAndUnsupportedInput(t *testing.T) {
	recognizer := NewDefaultRecognizer()
	request := &types.RecognitionRequest{
		DocumentID:       uuid.New(),
		OriginalFilename: "paper.pdf",
		ContentType:      "application/pdf",
		InputMode:        types.RecognitionInputFilePath,
		FilePath:         "/data/uploads/paper.pdf",
	}
	_, err := recognizer.Read(context.Background(), request)
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("Read() error = %v, want ErrUnsupportedContentType", err)
	}
}
