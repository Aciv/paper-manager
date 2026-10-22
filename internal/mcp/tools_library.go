package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
)

type librarySearchInput struct {
	Query    string   `json:"query"`
	DOI      string   `json:"doi"`
	Tags     []string `json:"tags"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
}

type librarySearchItem struct {
	Document types.Document       `json:"document"`
	Metadata *types.PaperMetadata `json:"paperMetadata,omitempty"`
}

type librarySearchResult struct {
	Items    []librarySearchItem `json:"items"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
	Total    int64               `json:"total"`
}

func decodeToolArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: malformed arguments", errInvalidToolInput)
	}
	return nil
}

func normalizedPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	if size > 50 {
		size = 50
	}
	return page, size
}

func (s *ToolServer) librarySearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var input librarySearchInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	page, size := normalizedPage(input.Page, input.PageSize)
	query := strings.TrimSpace(input.Query)
	doi := strings.TrimSpace(input.DOI)
	if doi != "" {
		return s.searchDocuments(ctx, types.DocumentSearch{DOI: doi, Page: page, PageSize: size})
	}
	if id, err := uuid.Parse(query); err == nil {
		return s.searchDocuments(ctx, types.DocumentSearch{ID: &id, Page: page, PageSize: size})
	}
	if query != "" || len(input.Tags) > 0 {
		search := types.PaperSearch{
			Title: query, Tags: input.Tags, TagMode: types.PaperTagModeAll,
			Page: page, PageSize: size,
		}
		metadata, err := s.papers.SearchMetadata(ctx, search)
		if err != nil {
			return nil, err
		}
		if metadata.Total == 0 && query != "" && len(input.Tags) == 0 {
			search.Title = ""
			search.Author = query
			metadata, err = s.papers.SearchMetadata(ctx, search)
			if err != nil {
				return nil, err
			}
		}
		return s.metadataSearchResult(ctx, metadata)
	}
	return s.searchDocuments(ctx, types.DocumentSearch{Page: page, PageSize: size})
}

func (s *ToolServer) searchDocuments(ctx context.Context, search types.DocumentSearch) (librarySearchResult, error) {
	page, err := s.documents.SearchDocument(ctx, search)
	if err != nil {
		return librarySearchResult{}, err
	}
	ids := make([]string, 0, len(page.Items))
	for _, document := range page.Items {
		ids = append(ids, document.ID.String())
	}
	projections, err := s.documents.GetDocumentProjections(ctx, types.DocumentProjectionQuery{
		DocumentIDs: ids, IncludeMetadata: true,
	})
	if err != nil {
		return librarySearchResult{}, err
	}
	items := make([]librarySearchItem, 0, len(projections))
	for _, projection := range projections {
		items = append(items, librarySearchItem{Document: projection.Document, Metadata: projection.Metadata})
	}
	return librarySearchResult{Items: items, Page: page.Page, PageSize: page.PageSize, Total: page.Total}, nil
}

func (s *ToolServer) metadataSearchResult(ctx context.Context, page types.PaperMetadataPage) (librarySearchResult, error) {
	ids := make([]string, 0, len(page.Items))
	for _, metadata := range page.Items {
		ids = append(ids, metadata.DocumentID.String())
	}
	projections, err := s.documents.GetDocumentProjections(ctx, types.DocumentProjectionQuery{DocumentIDs: ids})
	if err != nil {
		return librarySearchResult{}, err
	}
	documents := make(map[string]types.Document, len(projections))
	for _, projection := range projections {
		documents[projection.Document.ID.String()] = projection.Document
	}
	items := make([]librarySearchItem, 0, len(page.Items))
	for index := range page.Items {
		metadata := page.Items[index]
		document, ok := documents[metadata.DocumentID.String()]
		if !ok {
			continue
		}
		items = append(items, librarySearchItem{Document: document, Metadata: &metadata})
	}
	return librarySearchResult{Items: items, Page: page.Page, PageSize: page.PageSize, Total: page.Total}, nil
}

type libraryGetInput struct {
	DocumentID string `json:"document_id"`
}

func (s *ToolServer) libraryGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var input libraryGetInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	document, err := s.documents.GetDocumentByID(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	metadata, err := s.papers.GetMetadata(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"document": document, "paperMetadata": metadata}, nil
}

type importURLInput struct {
	URL string `json:"url"`
	DOI string `json:"doi"`
}

func (s *ToolServer) libraryImportURL(ctx context.Context, raw json.RawMessage) (any, error) {
	var input importURLInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	document, err := s.documents.CreateDocumentFromURL(ctx, types.DocumentURLImportCommand{
		URL: input.URL, DOI: input.DOI,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"document": document}, nil
}

type importFileInput struct {
	Path string `json:"path"`
	DOI  string `json:"doi"`
}

func (s *ToolServer) libraryImportFile(ctx context.Context, raw json.RawMessage) (any, error) {
	var input importFileInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	path, err := s.resolveLocalImportPath(input.Path)
	if err != nil {
		return nil, err
	}
	if _, supported := fileservice.SupportedContentType(path); !supported {
		return nil, fmt.Errorf("%w: unsupported local file type", errInvalidToolInput)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: local file is not readable", errInvalidToolInput)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: path must name a regular file", errInvalidToolInput)
	}
	maxSize := s.cfg.MaxUploadSize
	if maxSize <= 0 {
		maxSize = fileservice.DefaultMaxUploadSize
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%w: local file exceeds the upload limit", errPayloadTooLarge)
	}
	document, err := s.documents.CreateDocumentFromLocalFile(ctx, path, input.DOI)
	if err != nil {
		return nil, err
	}
	return map[string]any{"document": document}, nil
}

// resolveLocalImportPath confines filesystem imports to the configured root.
// The returned path is the symlink-resolved real path; no caller-supplied
// absolute path escapes the root, including through nested symlinks.
func (s *ToolServer) resolveLocalImportPath(raw string) (string, error) {
	root := strings.TrimSpace(s.cfg.MCPLocalImportRoot)
	if root == "" {
		return "", fmt.Errorf("%w: local file import is disabled; set MCP_LOCAL_IMPORT_ROOT", errLocalImportDisabled)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: local import root is invalid", errLocalImportDisabled)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("%w: local import root is not accessible", errLocalImportDisabled)
	}
	rootInfo, err := os.Stat(rootReal)
	if err != nil || !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: local import root must be a directory", errLocalImportDisabled)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: path is required", errInvalidToolInput)
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootReal, candidate)
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: invalid local file path", errInvalidToolInput)
	}
	realPath, err := filepath.EvalSymlinks(candidateAbs)
	if err != nil {
		return "", fmt.Errorf("%w: local file does not exist", errInvalidToolInput)
	}
	relative, err := filepath.Rel(rootReal, realPath)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", fmt.Errorf("%w: path is outside MCP_LOCAL_IMPORT_ROOT", errLocalImportForbidden)
	}
	return realPath, nil
}

func parseToolPublishedAt(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("%w: published_at cannot be empty", errInvalidToolInput)
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: published_at must be YYYY-MM-DD or RFC3339", errInvalidToolInput)
}

type metadataAuthorInput struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation"`
}

type updateMetadataInput struct {
	DocumentID       string                 `json:"document_id"`
	OriginalFilename *string                `json:"original_filename"`
	DOI              *string                `json:"doi"`
	Title            *string                `json:"title"`
	Authors          *[]metadataAuthorInput `json:"authors"`
	Abstract         *string                `json:"abstract"`
	Venue            *string                `json:"venue"`
	Tags             *[]string              `json:"tags"`
	SourceName       *string                `json:"source_name"`
	SourceURL        *string                `json:"source_url"`
	PublishedAt      *string                `json:"published_at"`
	Volume           *string                `json:"volume"`
	Issue            *string                `json:"issue"`
	Pages            *string                `json:"pages"`
	Language         *string                `json:"language"`
}

func (s *ToolServer) libraryUpdateMetadata(ctx context.Context, raw json.RawMessage) (any, error) {
	var input updateMetadataInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	var document *types.Document
	if input.OriginalFilename != nil || input.DOI != nil {
		updated, err := s.documents.UpdateDocumentInfo(ctx, input.DocumentID, input.OriginalFilename, input.DOI)
		if err != nil {
			return nil, err
		}
		document = updated
	}
	update := types.PaperMetadataUpdate{
		Title: input.Title, Abstract: input.Abstract, Venue: input.Venue, Tags: input.Tags,
		SourceName: input.SourceName, SourceURL: input.SourceURL,
		Volume: input.Volume, Issue: input.Issue, Pages: input.Pages, Language: input.Language,
	}
	if input.PublishedAt != nil {
		publishedAt, err := parseToolPublishedAt(*input.PublishedAt)
		if err != nil {
			return nil, err
		}
		update.PublishedAt = &publishedAt
	}
	if input.Authors != nil {
		authors := make([]types.PaperAuthor, 0, len(*input.Authors))
		for _, author := range *input.Authors {
			authors = append(authors, types.PaperAuthor{Name: author.Name, Affiliation: author.Affiliation})
		}
		update.Authors = &authors
	}
	var metadata *types.PaperMetadata
	if update.HasDataChanges() {
		updated, err := s.papers.UpdateMetadata(ctx, input.DocumentID, update)
		if err != nil {
			return nil, err
		}
		metadata = updated
	}
	if metadata == nil {
		var err error
		metadata, err = s.papers.GetMetadata(ctx, input.DocumentID)
		if err != nil {
			return nil, err
		}
	}
	if document == nil {
		var err error
		document, err = s.documents.GetDocumentByID(ctx, input.DocumentID)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"document": document, "paperMetadata": metadata}, nil
}

type deleteInput struct {
	DocumentID string `json:"document_id"`
	Confirm    bool   `json:"confirm"`
}

func (s *ToolServer) libraryDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var input deleteInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if !input.Confirm {
		return nil, fmt.Errorf("%w: confirm=true is required", errInvalidToolInput)
	}
	if err := s.documents.DeleteDocument(ctx, input.DocumentID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "documentId": input.DocumentID}, nil
}

type documentIDInput struct {
	DocumentID string `json:"document_id"`
}

func (s *ToolServer) processingStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input documentIDInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	document, err := s.documents.GetDocumentByID(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if s.processing == nil {
		states := make([]types.DocumentStageState, 0, 4)
		for _, stage := range []types.DocumentStage{
			types.DocumentStageMarkdown, types.DocumentStageMetadataExtraction,
			types.DocumentStageSourceLookup, types.DocumentStageReferenceLookup,
		} {
			if state, stateErr := document.StageState(stage); stateErr == nil {
				states = append(states, state)
			}
		}
		return map[string]any{"processing": types.DocumentProcessingView{
			DocumentID: document.ID, SourceState: document.SourceState,
			CanonicalDocumentID: document.CanonicalDocumentID, StageStates: states,
		}}, nil
	}
	view, err := s.processing.GetView(ctx, *document)
	if err != nil {
		return nil, err
	}
	if progress, progressErr := s.documents.GetReferenceProgress(ctx, document.ID.String()); progressErr == nil {
		view.ReferenceProgress = &progress
	}
	return map[string]any{"processing": view}, nil
}
