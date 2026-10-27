package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type documentReadingService struct {
	repo      interfaces.DocumentRepository
	storage   interfaces.FileStorage
	structure interfaces.DocumentStructureService
	renderer  interfaces.PDFPageRenderer
}

func NewDocumentReadingService(
	repo interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	structure interfaces.DocumentStructureService,
) interfaces.DocumentReadingService {
	return &documentReadingService{repo: repo, storage: storage, structure: structure}
}

// NewDocumentReadingServiceWithRenderer wires the optional page renderer. The
// three-argument constructor remains for focused tests and default-mode
// deployments that do not expose a parser renderer.
func NewDocumentReadingServiceWithRenderer(
	repo interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	structure interfaces.DocumentStructureService,
	renderer interfaces.PDFPageRenderer,
) interfaces.DocumentReadingService {
	return &documentReadingService{
		repo: repo, storage: storage, structure: structure, renderer: renderer,
	}
}

func (s *documentReadingService) OpenDocumentFile(
	ctx context.Context, id string,
) (*types.Document, io.ReadCloser, string, error) {
	document, err := s.getAvailableDocument(ctx, id)
	if err != nil {
		return nil, nil, "", err
	}
	reader, err := s.storage.Open(ctx, document.StorageKey)
	if err != nil {
		logger.Error(ctx, "open document file failed", "document_id", document.ID, "error", err)
		return nil, nil, "", err
	}
	return &document, reader, document.OriginalFilename, nil
}

func (s *documentReadingService) GetDocumentBlocks(
	ctx context.Context, id string, pageNumber int,
) ([]types.Block, error) {
	if pageNumber < 0 {
		return nil, errors.New("page number must not be negative")
	}
	documentID, err := parseDocumentID(id)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.Get(ctx, documentID); err != nil {
		return nil, err
	}
	return s.repo.ListBlocks(ctx, documentID, pageNumber)
}

func (s *documentReadingService) GetDocumentBlockImage(
	ctx context.Context, documentID, blockID string,
) (io.ReadCloser, string, error) {
	documentUUID, err := parseDocumentID(documentID)
	if err != nil {
		return nil, "", err
	}
	blockUUID, err := uuid.Parse(strings.TrimSpace(blockID))
	if err != nil {
		return nil, "", fmt.Errorf("invalid block id: %w", err)
	}
	block, err := s.repo.GetBlock(ctx, documentUUID, blockUUID)
	if err != nil {
		return nil, "", err
	}
	if block.AssetKey == "" {
		return nil, "", sql.ErrNoRows
	}
	reader, err := s.storage.Open(ctx, block.AssetKey)
	if err != nil {
		logger.Error(ctx, "open document block image failed",
			"document_id", documentUUID, "block_id", blockUUID, "storage_key", block.AssetKey, "error", err)
		return nil, "", err
	}
	return reader, filepath.Base(block.AssetKey), nil
}

func (s *documentReadingService) RenderPage(
	ctx context.Context,
	documentID string,
	pageNumber int,
	bbox *types.NormalizedPDFRect,
	dpi int,
) (*types.RenderedPDFPage, error) {
	if s.renderer == nil {
		return nil, types.ErrPDFRenderUnavailable
	}
	document, err := s.getAvailableDocument(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if pageNumber < 1 {
		return nil, fmt.Errorf("%w: page number must be positive", types.ErrInvalidPDFRenderRequest)
	}
	if dpi == 0 {
		dpi = types.DefaultPDFRenderDPI
	}
	if dpi < 1 || dpi > types.MaxPDFRenderDPI {
		return nil, fmt.Errorf(
			"%w: dpi must be between 1 and %d",
			types.ErrInvalidPDFRenderRequest, types.MaxPDFRenderDPI,
		)
	}
	if bbox != nil {
		if err := bbox.Validate(); err != nil {
			return nil, err
		}
	}
	renderCtx, cancel := context.WithTimeout(ctx, types.PDFRenderRequestTimeout)
	defer cancel()
	result, err := s.renderer.RenderPage(renderCtx, types.PDFPageRenderRequest{
		DocumentID:            document.ID.String(),
		SourceStorageKey:      document.StorageKey,
		PageNumber:            pageNumber,
		FullPage:              bbox == nil,
		BBox:                  bbox,
		DPI:                   dpi,
		RecognitionGeneration: document.MarkdownGeneration,
	})
	if err != nil {
		return nil, err
	}
	if result == nil || len(result.PNG) == 0 {
		return nil, errors.New("PDF renderer returned an empty page")
	}
	if len(result.PNG) > types.MaxPDFRenderOutputBytes {
		return nil, types.ErrPDFRenderTooLarge
	}
	if !bytes.HasPrefix(result.PNG, pngSignature) {
		return nil, errors.New("PDF renderer returned a non-PNG page")
	}
	if result.Width <= 0 || result.Height <= 0 ||
		int64(result.Width)*int64(result.Height) > types.MaxPDFRenderPixels {
		return nil, types.ErrPDFRenderTooLarge
	}
	if result.ContentType == "" {
		result.ContentType = "image/png"
	}
	logger.Info(ctx, "document page rendered",
		"document_id", document.ID,
		"page", pageNumber,
		"dpi", dpi,
		"bytes", len(result.PNG),
		"width", result.Width,
		"height", result.Height)
	return result, nil
}

func (s *documentReadingService) GetDocumentMarkdown(
	ctx context.Context, id string,
) (types.DocumentMarkdownContent, error) {
	document, err := s.getAvailableDocument(ctx, id)
	if err != nil {
		return types.DocumentMarkdownContent{}, err
	}
	manifest, err := s.repo.GetMarkdown(ctx, document.ID)
	if err != nil {
		return types.DocumentMarkdownContent{}, err
	}
	if manifest.StorageKey == "" {
		return types.DocumentMarkdownContent{}, sql.ErrNoRows
	}
	reader, err := s.storage.Open(ctx, manifest.StorageKey)
	if err != nil {
		logger.Error(ctx, "open document Markdown failed",
			"document_id", document.ID, "storage_key", manifest.StorageKey, "error", err)
		return types.DocumentMarkdownContent{}, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, types.MaxDocumentMarkdownBytes+1))
	if err != nil {
		return types.DocumentMarkdownContent{}, err
	}
	if len(content) > types.MaxDocumentMarkdownBytes {
		return types.DocumentMarkdownContent{}, types.ErrDocumentMarkdownTooLarge
	}
	contentType := strings.TrimSpace(manifest.ContentType)
	if contentType == "" {
		contentType = "text/markdown; charset=utf-8"
	}
	return types.DocumentMarkdownContent{
		DocumentID:            document.ID,
		Filename:              manifest.Filename,
		ContentType:           contentType,
		SizeBytes:             int64(len(content)),
		PageCount:             manifest.PageCount,
		RecognitionGeneration: manifest.RecognitionGeneration,
		Markdown:              content,
	}, nil
}

func (s *documentReadingService) GetDocumentReadingStructure(
	ctx context.Context, id string,
) (types.DocumentReadingStructure, error) {
	return s.structure.GetDocumentReadingStructure(ctx, id)
}

func (s *documentReadingService) getAvailableDocument(ctx context.Context, rawID string) (types.Document, error) {
	documentID, err := parseDocumentID(rawID)
	if err != nil {
		return types.Document{}, err
	}
	document, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return types.Document{}, err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return types.Document{}, sql.ErrNoRows
	}
	return document, nil
}

func parseDocumentID(raw string) (uuid.UUID, error) {
	documentID, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid document id: %w", err)
	}
	return documentID, nil
}

// GetDocumentPageCount reads only the persisted manifest, never the Markdown file.
func (s *documentReadingService) GetDocumentPageCount(ctx context.Context, id string) (int, error) {
	documentID, err := parseDocumentID(id)
	if err != nil {
		return 0, err
	}
	manifest, err := s.repo.GetMarkdown(ctx, documentID)
	return manifest.PageCount, err
}

func (s *documentReadingService) QueryDocumentBlocks(
	ctx context.Context, id string, query types.DocumentBlockQuery,
) (types.DocumentBlockPage, error) {
	documentID, err := parseDocumentID(id)
	if err != nil {
		return types.DocumentBlockPage{}, err
	}
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 50
	}
	if query.Mode == "" {
		query.Mode = "blocks"
	}
	selectors := 0
	for _, selected := range []bool{
		query.SectionOrdinal != nil, query.Range != nil, len(query.BlockIDs) > 0, query.PageNumber > 0,
	} {
		if selected {
			selectors++
		}
	}
	if selectors > 1 || query.Page < 1 || query.Page > 10000 || query.PageSize < 1 || query.PageSize > 200 ||
		(query.Mode != "blocks" && query.Mode != "range") || query.PageNumber < 0 || len(query.BlockIDs) > 200 ||
		(query.SectionOrdinal != nil && *query.SectionOrdinal < 0) ||
		(query.Range != nil && (query.Range.Start < 0 || query.Range.End < query.Range.Start)) ||
		(query.ExpectedGeneration != nil && *query.ExpectedGeneration < 0) {
		return types.DocumentBlockPage{}, types.ErrInvalidBlockQuery
	}
	seen := make(map[uuid.UUID]bool)
	for index, id := range query.BlockIDs {
		parsed, err := uuid.Parse(id)
		if err != nil || seen[parsed] {
			return types.DocumentBlockPage{}, types.ErrInvalidBlockQuery
		}
		seen[parsed] = true
		query.BlockIDs[index] = parsed.String()
	}
	return s.repo.QueryBlocks(ctx, documentID, query)
}

// GetDocumentPageCounts supports evidence batches without per-link database reads.
func (s *documentReadingService) GetDocumentPageCounts(
	ctx context.Context, ids []string,
) (map[string]int, error) {
	if len(ids) > 100 {
		return nil, types.ErrInvalidBlockQuery
	}
	parsed := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		value, err := parseDocumentID(id)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, value)
	}
	counts, err := s.repo.MarkdownPageCounts(ctx, parsed)
	if err != nil {
		return nil, err
	}
	result := make(map[string]int, len(counts))
	for id, count := range counts {
		result[id.String()] = count
	}
	return result, nil
}
