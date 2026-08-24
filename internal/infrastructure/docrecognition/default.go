// Package docrecognition contains infrastructure adapters that convert source
// documents to the normalized Markdown representation used by the application.
package docrecognition

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

var ErrUnsupportedContentType = types.ErrUnsupportedRecognitionContentType

// DefaultRecognizer converts text-native formats in-process. It never opens a
// caller-provided path or URL, so the local adapter accepts bytes only.
type DefaultRecognizer struct{}

// NewDefaultRecognizer constructs the built-in text converter.
func NewDefaultRecognizer() *DefaultRecognizer { return &DefaultRecognizer{} }

// Read converts TXT, Markdown, JSON, and CSV bytes to normalized Markdown.
func (r *DefaultRecognizer) Read(ctx context.Context, request *types.RecognitionRequest) (*types.RecognitionResult, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: request is required", types.ErrInvalidRecognitionRequest)
	}
	if err := request.Validate(); err != nil {
		logger.Error(ctx, "validate default recognition request failed", "document_id", request.DocumentID, "error", err)
		return nil, err
	}
	if request.InputMode != types.RecognitionInputBytes {
		err := fmt.Errorf("%w: default recognizer requires bytes input", ErrUnsupportedContentType)
		logger.Error(ctx, "default document recognition failed", "document_id", request.DocumentID, "error", err)
		return nil, err
	}

	markdown, err := defaultMarkdown(request)
	if err != nil {
		logger.Error(ctx, "default document recognition failed", "document_id", request.DocumentID, "error", err)
		return nil, err
	}
	result := &types.RecognitionResult{
		OutputMode: types.RecognitionOutputMarkdownBytes,
		Markdown:   markdown,
		Metadata: types.RecognitionMetadata{
			MarkdownFilename: markdownFilename(request.OriginalFilename),
			ContentType:      "text/markdown",
			SizeBytes:        int64(len(markdown)),
			ParserName:       DriverDefault,
		},
	}
	logger.Info(ctx, "default document recognition completed", "document_id", request.DocumentID, "size_bytes", result.Metadata.SizeBytes)
	return result, nil
}

func defaultMarkdown(request *types.RecognitionRequest) ([]byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.ContentType, ";")[0]))
	switch contentType {
	case "text/plain", "text/markdown":
		return append([]byte(nil), request.Content...), nil
	case "application/json":
		var value any
		if err := json.Unmarshal(request.Content, &value); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
		formatted, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("format JSON: %w", err)
		}
		return []byte("```json\n" + string(formatted) + "\n```\n"), nil
	case "text/csv":
		return csvMarkdown(request.Content)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedContentType, contentType)
	}
}

func csvMarkdown(content []byte) ([]byte, error) {
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("decode CSV: %w", err)
	}
	if len(records) == 0 || len(records[0]) == 0 {
		return nil, errors.New("CSV has no columns")
	}
	columns := len(records[0])
	var markdown strings.Builder
	writeCSVMarkdownRow(&markdown, records[0], columns)
	markdown.WriteByte('|')
	for i := 0; i < columns; i++ {
		markdown.WriteString(" --- |")
	}
	markdown.WriteByte('\n')
	for _, row := range records[1:] {
		writeCSVMarkdownRow(&markdown, row, columns)
	}
	return []byte(markdown.String()), nil
}

func writeCSVMarkdownRow(markdown *strings.Builder, row []string, columns int) {
	markdown.WriteByte('|')
	for i := 0; i < columns; i++ {
		cell := ""
		if i < len(row) {
			cell = strings.ReplaceAll(row[i], "|", "\\|")
			cell = strings.ReplaceAll(cell, "\n", "<br>")
		}
		markdown.WriteByte(' ')
		markdown.WriteString(cell)
		markdown.WriteString(" |")
	}
	markdown.WriteByte('\n')
}

func markdownFilename(filename string) string {
	name := filepath.Base(strings.TrimSpace(filename))
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if base == "" || base == "." {
		base = "document"
	}
	return base + ".md"
}

var _ interfaces.DocumentRecognizer = (*DefaultRecognizer)(nil)
