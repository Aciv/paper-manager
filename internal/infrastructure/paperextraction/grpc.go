// Package paperextraction adapts the Python paper-extraction gRPC service to
// application-owned paper metadata contracts.
package paperextraction

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCExtractor obtains deterministic extraction candidates from the bundled
// Python process. The caller owns deadlines and cancellation through ctx.
type GRPCExtractor struct {
	client papersv1.PaperMetadataExtractorClient
	conn   *grpc.ClientConn
}

// NewGRPCExtractor connects a paper extractor to the configured parser
// address. grpc.NewClient is non-blocking; Extract reports unavailable peers.
func NewGRPCExtractor(address string, maxMessageSize int) (*GRPCExtractor, error) {
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("PARSER_GRPC_ADDR is required for paper extraction")
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
		return nil, fmt.Errorf("create paper extraction client: %w", err)
	}
	return &GRPCExtractor{
		client: papersv1.NewPaperMetadataExtractorClient(conn),
		conn:   conn,
	}, nil
}

// NewGRPCExtractorWithClient supports focused adapter tests without a live
// Python process.
func NewGRPCExtractorWithClient(
	client papersv1.PaperMetadataExtractorClient,
) *GRPCExtractor {
	return &GRPCExtractor{client: client}
}

// Close releases the connection created by NewGRPCExtractor.
func (extractor *GRPCExtractor) Close() error {
	if extractor == nil || extractor.conn == nil {
		return nil
	}
	return extractor.conn.Close()
}

// Extract validates and maps one exact Markdown revision to the protobuf
// contract, then validates the candidates returned by Python.
func (extractor *GRPCExtractor) Extract(
	ctx context.Context,
	request *types.PaperExtractionRequest,
) (*types.PaperExtractionResult, error) {
	if extractor == nil || extractor.client == nil {
		return nil, errors.New("paper metadata extractor is not configured")
	}
	if request == nil {
		return nil, fmt.Errorf("%w: request is required", types.ErrInvalidPaperExtraction)
	}
	if err := request.Validate(); err != nil {
		logger.Error(ctx, "validate paper extraction request failed",
			"document_id", request.DocumentID,
			"error", err)
		return nil, err
	}
	rpcRequest, err := mapExtractionRequest(request)
	if err != nil {
		return nil, err
	}
	response, err := extractor.client.Extract(ctx, rpcRequest)
	if err != nil {
		logger.Error(ctx, "paper metadata extraction call failed",
			"document_id", request.DocumentID,
			"error", err)
		return nil, fmt.Errorf("extract paper metadata through Python: %w", err)
	}
	if response == nil {
		return nil, errors.New("paper metadata extractor returned an empty response")
	}
	result, err := mapExtractionResult(response)
	if err != nil {
		logger.Error(ctx, "validate paper extraction response failed",
			"document_id", request.DocumentID,
			"error", err)
		return nil, err
	}
	logger.Info(ctx, "paper metadata extraction completed",
		"document_id", request.DocumentID,
		"extractor", result.ExtractorName,
		"author_count", len(result.Authors),
		"reference_count", len(result.References))
	return result, nil
}

func mapExtractionRequest(
	request *types.PaperExtractionRequest,
) (*papersv1.PaperExtractionRequest, error) {
	rpcBlocks := make([]*papersv1.PaperSourceBlock, 0, len(request.Blocks))
	for _, block := range request.Blocks {
		if block.Ordinal > math.MaxInt32 {
			return nil, fmt.Errorf(
				"%w: block ordinal %d exceeds protobuf range",
				types.ErrInvalidPaperExtraction,
				block.Ordinal,
			)
		}
		rpcBlock := &papersv1.PaperSourceBlock{
			Ordinal: int32(block.Ordinal),
			Type:    block.Type,
			Text:    block.Text,
		}
		location, err := mapSourceLocation(block.Location)
		if err != nil {
			return nil, err
		}
		if location != nil {
			rpcBlock.Location = location
		}
		rpcBlocks = append(rpcBlocks, rpcBlock)
	}
	return &papersv1.PaperExtractionRequest{
		DocumentId: request.DocumentID.String(),
		Markdown:   append([]byte(nil), request.Markdown...),
		Blocks:     rpcBlocks,
	}, nil
}

func mapSourceLocation(
	location *types.PDFBlockLocation,
) (*papersv1.PaperSourceLocation, error) {
	if location == nil {
		return nil, nil
	}
	if *location.PageNumber > math.MaxInt32 ||
		*location.MarkdownCodepointStart > math.MaxInt32 ||
		*location.MarkdownCodepointEnd > math.MaxInt32 {
		return nil, fmt.Errorf(
			"%w: source location exceeds protobuf range",
			types.ErrInvalidPaperExtraction,
		)
	}
	return &papersv1.PaperSourceLocation{
		PageNumber:             int32(*location.PageNumber),
		PageWidth:              *location.PageWidth,
		PageHeight:             *location.PageHeight,
		BboxX0:                 *location.BBoxX0,
		BboxY0:                 *location.BBoxY0,
		BboxX1:                 *location.BBoxX1,
		BboxY1:                 *location.BBoxY1,
		MarkdownCodepointStart: int32(*location.MarkdownCodepointStart),
		MarkdownCodepointEnd:   int32(*location.MarkdownCodepointEnd),
	}, nil
}

func mapExtractionResult(
	response *papersv1.PaperExtractionResult,
) (*types.PaperExtractionResult, error) {
	result := &types.PaperExtractionResult{
		References:       append([]string(nil), response.GetReferences()...),
		ExtractorName:    strings.TrimSpace(response.GetExtractorName()),
		ExtractorVersion: strings.TrimSpace(response.GetExtractorVersion()),
		Authors:          make([]types.PaperTextCandidate, 0, len(response.GetAuthors())),
	}
	if response.GetTitle() != nil {
		result.Title = mapTextCandidate(response.GetTitle())
	}
	for index, author := range response.GetAuthors() {
		if author == nil {
			return nil, fmt.Errorf(
				"%w: author %d is empty",
				types.ErrInvalidPaperExtraction,
				index,
			)
		}
		result.Authors = append(result.Authors, *mapTextCandidate(author))
	}
	if response.GetAbstract() != nil {
		result.Abstract = mapTextCandidate(response.GetAbstract())
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func mapTextCandidate(candidate *papersv1.PaperTextCandidate) *types.PaperTextCandidate {
	evidence := make([]types.PaperSourceEvidence, 0, len(candidate.GetEvidence()))
	for _, item := range candidate.GetEvidence() {
		if item == nil {
			continue
		}
		evidence = append(evidence, types.PaperSourceEvidence{
			BlockOrdinal:           int(item.GetBlockOrdinal()),
			PageNumber:             int(item.GetPageNumber()),
			MarkdownCodepointStart: int(item.GetMarkdownCodepointStart()),
			MarkdownCodepointEnd:   int(item.GetMarkdownCodepointEnd()),
		})
	}
	return &types.PaperTextCandidate{
		Value:      candidate.GetValue(),
		Confidence: candidate.GetConfidence(),
		Evidence:   evidence,
	}
}

var _ interfaces.PaperMetadataExtractor = (*GRPCExtractor)(nil)
