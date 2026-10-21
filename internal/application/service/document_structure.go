package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	documentReadingStructureSchemaVersion = 2
	documentStructureCompileTimeout       = 30 * time.Second
)

var ErrDocumentStructureTooLarge = errors.New("document structure exceeds response limit")

type documentStructureService struct {
	repo       interfaces.DocumentRepository
	cacheDir   string
	maxEntries int
	maxBytes   int64
	flights    singleflight.Group
	reads      atomic.Uint64
}

func NewDocumentStructureService(cfg config.Config, repo interfaces.DocumentRepository) interfaces.DocumentStructureService {
	cacheDir := strings.TrimSpace(cfg.DocumentStructureCacheDir)
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "papers-manager", "document-structures")
	}
	service := &documentStructureService{
		repo: repo, cacheDir: filepath.Clean(cacheDir),
		maxEntries: cfg.DocumentStructureCacheEntries, maxBytes: cfg.DocumentStructureCacheMaxBytes,
	}
	if service.maxEntries <= 0 {
		service.maxEntries = 1000
	}
	if service.maxBytes <= 0 {
		service.maxBytes = 16 << 20
	}
	service.cleanupCache(context.Background())
	return service
}

func (s *documentStructureService) GetDocumentReadingStructure(ctx context.Context, rawID string) (types.DocumentReadingStructure, error) {
	documentID, err := uuid.Parse(strings.TrimSpace(rawID))
	if err != nil {
		return types.DocumentReadingStructure{}, fmt.Errorf("invalid document id: %w", err)
	}
	result := s.flights.DoChan(documentID.String(), func() (any, error) {
		// The shared compile must not inherit the first caller's cancellation:
		// each waiter can stop independently while this bounded operation remains
		// useful to callers that are still waiting.
		operationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), documentStructureCompileTimeout)
		defer cancel()
		return s.loadDocumentReadingStructure(operationCtx, documentID)
	})
	select {
	case <-ctx.Done():
		return types.DocumentReadingStructure{}, ctx.Err()
	case completed := <-result:
		if completed.Err != nil {
			return types.DocumentReadingStructure{}, completed.Err
		}
		structure, ok := completed.Val.(types.DocumentReadingStructure)
		if !ok {
			return types.DocumentReadingStructure{}, errors.New("document structure compile returned an invalid result")
		}
		return structure, nil
	}
}

func (s *documentStructureService) loadDocumentReadingStructure(ctx context.Context, documentID uuid.UUID) (types.DocumentReadingStructure, error) {
	document, err := s.repo.Get(ctx, documentID)
	if err != nil {
		return types.DocumentReadingStructure{}, err
	}
	manifest, err := s.repo.GetMarkdown(ctx, documentID)
	if err != nil {
		return types.DocumentReadingStructure{}, err
	}
	outline, err := types.ParseDocumentOutline(manifest.DocumentOutline)
	if err != nil {
		return types.DocumentReadingStructure{}, err
	}
	fingerprint := structureFingerprint(document.FileSHA256, manifest.RecognitionGeneration, outline.SchemaVersion)
	if cached, ok := s.readCache(documentID, fingerprint); ok {
		return cached, nil
	}
	diagnostics := append([]string(nil), outline.Diagnostics...)
	if outline.Source == types.DocumentOutlineUnavailable {
		diagnostics = append(diagnostics, "recognize_document_to_create_outline")
	}
	structure := types.DocumentReadingStructure{
		SchemaVersion: documentReadingStructureSchemaVersion,
		DocumentID:    documentID.String(), RecognitionGeneration: manifest.RecognitionGeneration,
		Fingerprint: fingerprint, Outline: outline, Diagnostics: diagnostics,
	}
	data, err := json.Marshal(structure)
	if err != nil {
		return types.DocumentReadingStructure{}, fmt.Errorf("encode document structure: %w", err)
	}
	if int64(len(data)) > s.maxBytes {
		return types.DocumentReadingStructure{}, ErrDocumentStructureTooLarge
	}
	if writeErr := s.writeCache(documentID, data); writeErr != nil {
		logger.Warn(ctx, "write document structure cache failed", "document_id", documentID, "error", writeErr)
	} else {
		s.cleanupCache(ctx)
	}
	if s.reads.Add(1)%64 == 0 {
		s.cleanupCache(ctx)
	}
	return structure, nil
}

func structureFingerprint(fileSHA string, generation int64, outlineSchema int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%d", strings.ToLower(fileSHA), generation, outlineSchema, documentReadingStructureSchemaVersion)))
	return hex.EncodeToString(sum[:])
}

func (s *documentStructureService) cachePath(id uuid.UUID) string {
	return filepath.Join(s.cacheDir, id.String()+".json")
}

func (s *documentStructureService) readCache(id uuid.UUID, fingerprint string) (types.DocumentReadingStructure, bool) {
	data, err := os.ReadFile(s.cachePath(id))
	if err != nil || int64(len(data)) > s.maxBytes {
		return types.DocumentReadingStructure{}, false
	}
	var structure types.DocumentReadingStructure
	if json.Unmarshal(data, &structure) != nil || structure.SchemaVersion != documentReadingStructureSchemaVersion ||
		structure.DocumentID != id.String() || structure.Fingerprint != fingerprint {
		return types.DocumentReadingStructure{}, false
	}
	return structure, true
}

func (s *documentStructureService) writeCache(id uuid.UUID, data []byte) error {
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.cacheDir, id.String()+"-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.cachePath(id))
}

func (s *documentStructureService) cleanupCache(ctx context.Context) {
	entries, err := os.ReadDir(s.cacheDir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		logger.Warn(ctx, "read document structure cache directory failed", "error", err)
		return
	}
	type cachedFile struct {
		path string
		mod  int64
	}
	files := make([]cachedFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, statErr := entry.Info()
		if statErr == nil {
			files = append(files, cachedFile{filepath.Join(s.cacheDir, entry.Name()), info.ModTime().UnixNano()})
		}
	}
	if len(files) <= s.maxEntries {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, file := range files[:len(files)-s.maxEntries] {
		if removeErr := os.Remove(file.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			logger.Warn(ctx, "evict document structure cache failed", "path", file.path, "error", removeErr)
		}
	}
}
