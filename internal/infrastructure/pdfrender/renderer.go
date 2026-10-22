// Package pdfrender adapts the Python PdfRenderer gRPC service to the
// application-owned page-renderer contract.
package pdfrender

import (
	"context"
	"errors"
	"fmt"
	"strings"

	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type GRPCRenderer struct {
	client papersv1.PdfRendererClient
	conn   *grpc.ClientConn
}

type unavailableRenderer struct {
	reason string
}

func (renderer *unavailableRenderer) RenderPage(
	context.Context, types.PDFPageRenderRequest,
) (*types.RenderedPDFPage, error) {
	return nil, fmt.Errorf("%w: %s", types.ErrPDFRenderUnavailable, renderer.reason)
}

// NewRenderer connects to the existing parser gRPC server. An empty parser
// address yields a disabled adapter rather than failing application startup.
func NewRenderer(cfg config.Config) (interfaces.PDFPageRenderer, error) {
	address := strings.TrimSpace(cfg.ParserGRPCAddress)
	if address == "" {
		return &unavailableRenderer{reason: "PARSER_GRPC_ADDR is empty"}, nil
	}
	if cfg.ParserGRPCMaxMessageSize <= 0 {
		return nil, errors.New("PARSER_GRPC_MAX_MESSAGE_SIZE must be positive")
	}
	renderRecvLimit := min(
		int(types.MaxPDFRenderOutputBytes)+(1<<20),
		cfg.ParserGRPCMaxMessageSize,
	)
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(1<<20),
			grpc.MaxCallRecvMsgSize(renderRecvLimit),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create PDF renderer client: %w", err)
	}
	return &GRPCRenderer{
		client: papersv1.NewPdfRendererClient(conn),
		conn:   conn,
	}, nil
}

func (renderer *GRPCRenderer) RenderPage(
	ctx context.Context,
	request types.PDFPageRenderRequest,
) (*types.RenderedPDFPage, error) {
	if renderer == nil || renderer.client == nil {
		return nil, types.ErrPDFRenderUnavailable
	}
	if strings.TrimSpace(request.SourceStorageKey) == "" {
		return nil, fmt.Errorf("%w: source storage key is required", types.ErrInvalidPDFRenderRequest)
	}
	if request.PageNumber < 1 {
		return nil, fmt.Errorf("%w: page number must be positive", types.ErrInvalidPDFRenderRequest)
	}
	if request.DPI < 1 || request.DPI > types.MaxPDFRenderDPI {
		return nil, fmt.Errorf("%w: dpi is outside the supported range", types.ErrInvalidPDFRenderRequest)
	}
	if request.BBox != nil {
		if err := request.BBox.Validate(); err != nil {
			return nil, err
		}
	}
	protoRequest := &papersv1.PdfRenderRequest{
		DocumentId:            request.DocumentID,
		SourceStorageKey:      request.SourceStorageKey,
		PageNumber:            int32(request.PageNumber),
		FullPage:              request.FullPage,
		Dpi:                   int32(request.DPI),
		RecognitionGeneration: request.RecognitionGeneration,
	}
	if request.BBox != nil {
		protoRequest.Bbox = &papersv1.PdfNormalizedRect{
			X0: request.BBox.X0,
			Y0: request.BBox.Y0,
			X1: request.BBox.X1,
			Y1: request.BBox.Y1,
		}
	}
	response, err := renderer.client.RenderPage(ctx, protoRequest)
	if err != nil {
		logger.Error(ctx, "PDF render RPC failed",
			"document_id", request.DocumentID,
			"page", request.PageNumber,
			"error", err)
		switch status.Code(err) {
		case codes.InvalidArgument, codes.OutOfRange, codes.PermissionDenied:
			return nil, fmt.Errorf("%w: %v", types.ErrInvalidPDFRenderRequest, err)
		case codes.ResourceExhausted:
			return nil, fmt.Errorf("%w: %v", types.ErrPDFRenderTooLarge, err)
		case codes.Unavailable, codes.DeadlineExceeded:
			return nil, fmt.Errorf("%w: %v", types.ErrPDFRenderUnavailable, err)
		default:
			return nil, fmt.Errorf("render PDF page: %w", err)
		}
	}
	if response == nil {
		return nil, errors.New("PDF renderer returned an empty response")
	}
	if len(response.Png) > types.MaxPDFRenderOutputBytes {
		return nil, types.ErrPDFRenderTooLarge
	}
	if response.Width <= 0 || response.Height <= 0 ||
		int64(response.Width)*int64(response.Height) > types.MaxPDFRenderPixels {
		return nil, types.ErrPDFRenderTooLarge
	}
	return &types.RenderedPDFPage{
		PNG:         response.Png,
		ContentType: response.ContentType,
		Width:       int(response.Width),
		Height:      int(response.Height),
		PageWidth:   response.PageWidth,
		PageHeight:  response.PageHeight,
	}, nil
}

func (renderer *GRPCRenderer) Close() error {
	if renderer == nil || renderer.conn == nil {
		return nil
	}
	return renderer.conn.Close()
}

var _ interfaces.PDFPageRenderer = (*GRPCRenderer)(nil)
var _ interfaces.PDFPageRenderer = (*unavailableRenderer)(nil)
