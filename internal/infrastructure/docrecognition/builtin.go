package docrecognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// BuiltinRecognizer delegates PDF recognition to the repository's bundled
// Python gRPC parser. The caller controls deadlines through ctx.
type BuiltinRecognizer struct {
	client papersv1.DocumentParserClient
	conn   *grpc.ClientConn
}

// NewBuiltinRecognizer connects the bundled Python parser client. grpc.NewClient
// is non-blocking; Read reports unavailable servers through its returned error.
func NewBuiltinRecognizer(address string, maxMessageSize int) (*BuiltinRecognizer, error) {
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("PARSER_GRPC_ADDR is required for builtin recognizer")
	}
	if maxMessageSize <= 0 {
		return nil, errors.New("PARSER_GRPC_MAX_MESSAGE_SIZE must be positive")
	}
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(maxMessageSize),
			grpc.MaxCallRecvMsgSize(maxMessageSize),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create builtin parser client: %w", err)
	}
	return &BuiltinRecognizer{client: papersv1.NewDocumentParserClient(conn), conn: conn}, nil
}

// NewBuiltinRecognizerWithClient creates a recognizer around an injected gRPC
// client. It supports isolated adapter tests without a live Python server.
func NewBuiltinRecognizerWithClient(client papersv1.DocumentParserClient) *BuiltinRecognizer {
	return &BuiltinRecognizer{client: client}
}

// Close releases the gRPC connection created by NewBuiltinRecognizer.
func (r *BuiltinRecognizer) Close() error {
	if r == nil || r.conn == nil {
		return nil
	}
	return r.conn.Close()
}

// Read sends exactly one authorized source to the Python parser and maps its
// response into the application-owned recognition result.
func (r *BuiltinRecognizer) Read(ctx context.Context, request *types.RecognitionRequest) (*types.RecognitionResult, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("builtin recognizer is not configured")
	}
	if request == nil {
		return nil, fmt.Errorf("%w: request is required", types.ErrInvalidRecognitionRequest)
	}
	if err := request.Validate(); err != nil {
		logger.Error(ctx, "validate builtin recognition request failed", "document_id", request.DocumentID, "error", err)
		return nil, err
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.ContentType, ";")[0]))
	if contentType != "application/pdf" {
		err := fmt.Errorf("%w: builtin recognizer supports application/pdf", ErrUnsupportedContentType)
		logger.Error(ctx, "builtin document recognition failed", "document_id", request.DocumentID, "error", err)
		return nil, err
	}

	rpcRequest := &papersv1.RecognitionRequest{
		DocumentId:  request.DocumentID.String(),
		Filename:    request.OriginalFilename,
		ContentType: request.ContentType,
	}
	switch request.InputMode {
	case types.RecognitionInputBytes:
		rpcRequest.Source = &papersv1.RecognitionRequest_Content{Content: request.Content}
	case types.RecognitionInputFilePath:
		rpcRequest.Source = &papersv1.RecognitionRequest_FilePath{FilePath: request.FilePath}
	case types.RecognitionInputURL:
		rpcRequest.Source = &papersv1.RecognitionRequest_SourceUrl{SourceUrl: request.URL}
	}

	response, err := r.client.Recognize(ctx, rpcRequest)
	if err != nil {
		logger.Error(ctx, "builtin document recognition call failed", "document_id", request.DocumentID, "error", err)
		return nil, fmt.Errorf("recognize document through builtin parser: %w", err)
	}
	if response == nil {
		return nil, errors.New("builtin parser returned an empty response")
	}
	if response.SizeBytes != int64(len(response.Markdown)) {
		return nil, fmt.Errorf("builtin parser result size mismatch: got %d bytes, metadata says %d", len(response.Markdown), response.SizeBytes)
	}
	if response.PageCount < 0 {
		return nil, fmt.Errorf("builtin parser returned negative page count %d", response.PageCount)
	}
	if response.ContentType != "" && response.ContentType != "text/markdown" {
		return nil, fmt.Errorf("builtin parser returned unsupported content type %q", response.ContentType)
	}
	sum := sha256.Sum256(response.Markdown)
	sha := hex.EncodeToString(sum[:])
	filename := markdownFilename(request.OriginalFilename)
	if candidate := filepath.Base(strings.TrimSpace(response.MarkdownFilename)); candidate != "" && candidate != "." && candidate != ".." {
		if safeName, err := utils.SafeFileName(candidate); err == nil && strings.EqualFold(filepath.Ext(safeName), ".md") {
			filename = safeName
		}
	}
	result := &types.RecognitionResult{
		OutputMode: types.RecognitionOutputMarkdownBytes,
		Markdown:   append([]byte(nil), response.Markdown...),
		Metadata: types.RecognitionMetadata{
			MarkdownFilename: filename,
			ContentType:      "text/markdown",
			SizeBytes:        response.SizeBytes,
			SHA256:           sha,
			Language:         response.Language,
			PageCount:        int(response.PageCount),
			ParserName:       response.ParserName,
			ParserVersion:    response.ParserVersion,
		},
		Blocks: mapBuiltinBlocks(response.GetBlocks()),
	}
	logger.Info(ctx, "builtin document recognition completed", "document_id", request.DocumentID,
		"parser", response.ParserName, "size_bytes", response.SizeBytes, "page_count", response.PageCount)
	return result, nil
}

func mapBuiltinBlocks(blocks []*papersv1.RecognitionBlock) []types.RecognitionBlock {
	result := make([]types.RecognitionBlock, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}
		pageNumber, ordinal := int(block.GetPageNumber()), int(block.GetOrdinal())
		pageWidth, pageHeight := block.GetPageWidth(), block.GetPageHeight()
		x0, y0, x1, y1 := block.GetBboxX0(), block.GetBboxY0(), block.GetBboxX1(), block.GetBboxY1()
		start, end := int(block.GetMarkdownCodepointStart()), int(block.GetMarkdownCodepointEnd())
		if ordinal < 0 || pageNumber < 1 || pageWidth <= 0 || pageHeight <= 0 ||
			x0 < 0 || y0 < 0 || x1 <= x0 || y1 <= y0 || x1 > pageWidth || y1 > pageHeight ||
			start < 0 || end < start || strings.TrimSpace(block.GetType()) == "" {
			continue
		}
		result = append(result, types.RecognitionBlock{
			Ordinal: ordinal,
			Type:    strings.TrimSpace(block.GetType()),
			Text:    block.GetText(),
			Location: types.PDFBlockLocation{
				PageNumber:             &pageNumber,
				PageWidth:              &pageWidth,
				PageHeight:             &pageHeight,
				BBoxX0:                 &x0,
				BBoxY0:                 &y0,
				BBoxX1:                 &x1,
				BBoxY1:                 &y1,
				MarkdownCodepointStart: &start,
				MarkdownCodepointEnd:   &end,
			},
		})
	}
	return result
}

var _ interfaces.DocumentRecognizer = (*BuiltinRecognizer)(nil)
