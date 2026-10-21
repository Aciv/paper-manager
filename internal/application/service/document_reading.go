package service

import (
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

type documentReadingService struct {
	repo      interfaces.DocumentRepository
	storage   interfaces.FileStorage
	structure interfaces.DocumentStructureService
}

func NewDocumentReadingService(
	repo interfaces.DocumentRepository,
	storage interfaces.FileStorage,
	structure interfaces.DocumentStructureService,
) interfaces.DocumentReadingService {
	return &documentReadingService{repo: repo, storage: storage, structure: structure}
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
