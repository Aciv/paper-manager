package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	appservice "paperMaster/internal/application/service"
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
	importOptionsInput
	URL       string `json:"url"`
	DOI       string `json:"doi"`
	BaseID    string `json:"base_id"`
	RequestID string `json:"request_id"`
}

// validateImportBase checks a requested base before any document is created,
// so an invalid base_id cannot leave a half-applied import behind.
func (s *ToolServer) validateImportBase(ctx context.Context, baseID string) error {
	baseID = strings.TrimSpace(baseID)
	if baseID == "" {
		return nil
	}
	if s.bases == nil {
		return fmt.Errorf("%w: base service is unavailable", errInvalidToolInput)
	}
	_, err := s.bases.GetBase(ctx, baseID)
	return err
}

func (s *ToolServer) attachImportedDocumentToBase(
	ctx context.Context, baseID string, document *types.Document,
) error {
	baseID = strings.TrimSpace(baseID)
	if baseID == "" {
		return nil
	}
	if document == nil {
		return fmt.Errorf("%w: imported document is missing", errInvalidToolInput)
	}
	if s.bases == nil {
		return fmt.Errorf("%w: base service is unavailable", errInvalidToolInput)
	}
	// A committed upload must not be stranded outside its requested Base just
	// because the client disconnected after the file was saved.
	attachCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.bases.AddDocumentToBase(attachCtx, baseID, document.ID.String())
}

func (s *ToolServer) libraryImportURL(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := validateMCPImportOptions(raw, 0, nil); err != nil {
		return nil, err
	}
	var input importURLInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if s.urlImports == nil {
		return nil, types.ErrTaskInspectorUnavailable
	}
	return s.urlImports.EnqueueURLs(ctx, []types.DocumentURLImportCommand{{URL: input.URL, DOI: input.DOI,
		Metadata: input.Metadata, Processing: input.Processing, DuplicateMetadata: input.DuplicateMetadata}},
		input.BaseID, input.RequestID)
}

type importFileInput struct {
	importOptionsInput
	Path   string `json:"path"`
	DOI    string `json:"doi"`
	BaseID string `json:"base_id"`
}

func (s *ToolServer) libraryImportFile(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := validateMCPImportOptions(raw, 0, nil); err != nil {
		return nil, err
	}
	var input importFileInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	path, err := s.validateLocalImportFile(input.Path)
	if err != nil {
		return nil, err
	}
	if err := s.validateImportBase(ctx, input.BaseID); err != nil {
		return nil, err
	}
	document, err := s.documents.CreateDocumentFromLocalFile(ctx, path, input.DOI, input.options()...)
	duplicate := errors.Is(err, types.ErrDuplicateDOI) || errors.Is(err, types.ErrDuplicateFileHash)
	queueFailed := errors.Is(err, appservice.ErrDocumentProcessQueue)
	if err != nil && !((duplicate || queueFailed) && document != nil) {
		return nil, err
	}
	if err := s.attachImportedDocumentToBase(ctx, input.BaseID, document); err != nil {
		return nil, err
	}
	result := map[string]any{"document": document}
	if duplicate {
		result["status"] = "duplicate"
	}
	if queueFailed {
		result["processingQueued"], result["retryable"] = false, true
	}
	if baseID := strings.TrimSpace(input.BaseID); baseID != "" {
		result["baseId"] = baseID
	}
	return result, nil
}

// validateImportBatch enforces the shared 1..20 item bound and the optional
// per-item DOI alignment used by both batch import tools.
func validateImportBatch(count, doiCount int, field string) error {
	if count == 0 || count > types.MaxDocumentBatchItems {
		return fmt.Errorf(
			"%w: %s must contain between 1 and %d items",
			errInvalidToolInput, field, types.MaxDocumentBatchItems,
		)
	}
	if doiCount != 0 && doiCount != count {
		return fmt.Errorf("%w: provide no dois or exactly one per item", errInvalidToolInput)
	}
	return nil
}

func batchDOI(dois []string, index int) string {
	if len(dois) == 0 {
		return ""
	}
	return strings.TrimSpace(dois[index])
}

type libraryImportBatchResult struct {
	types.DocumentBatchResult
	BaseID string `json:"baseId,omitempty"`
}

// attachImportBatchToBase applies base_id to every accepted or duplicate item.
// Attachment failures are reported per item because the documents already
// exist; they must not be reported as a failed import.
func (s *ToolServer) attachImportBatchToBase(
	ctx context.Context, baseID string, result types.DocumentBatchResult,
) libraryImportBatchResult {
	baseID = strings.TrimSpace(baseID)
	output := libraryImportBatchResult{DocumentBatchResult: result, BaseID: baseID}
	if baseID == "" {
		return output
	}
	attachFailed := false
	for index := range output.Items {
		item := &output.Items[index]
		if item.Document == nil ||
			(item.Status != types.DocumentImportItemAccepted && item.Status != types.DocumentImportItemDuplicate) {
			continue
		}
		if err := s.attachImportedDocumentToBase(ctx, baseID, item.Document); err != nil {
			item.Status = types.DocumentImportItemFailed
			item.Error = "document was imported but could not be added to the Base"
			attachFailed = true
		}
	}
	if attachFailed {
		message := "some documents could not be added to the Base"
		if output.Error == "" {
			output.Error = message
		} else {
			output.Error += "; " + message
		}
	}
	output.DocumentBatchResult.UpdateSummary()
	return output
}

type importURLsInput struct {
	Options   []types.DocumentImportOptions `json:"options"`
	URLs      []string                      `json:"urls"`
	DOIs      []string                      `json:"dois"`
	BaseID    string                        `json:"base_id"`
	RequestID string                        `json:"request_id"`
}

func (s *ToolServer) libraryImportURLs(ctx context.Context, raw json.RawMessage) (any, error) {
	var input importURLsInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := validateImportBatch(len(input.URLs), len(input.DOIs), "urls"); err != nil {
		return nil, err
	}
	if err := validateMCPImportOptions(raw, len(input.URLs), input.Options); err != nil {
		return nil, err
	}
	if s.urlImports == nil {
		return nil, types.ErrTaskInspectorUnavailable
	}
	commands := make([]types.DocumentURLImportCommand, len(input.URLs))
	for index, url := range input.URLs {
		commands[index] = types.DocumentURLImportCommand{URL: strings.TrimSpace(url), DOI: batchDOI(input.DOIs, index)}
		if len(input.Options) > 0 {
			option := input.Options[index]
			commands[index].Metadata = option.Metadata
			commands[index].Processing = option.Processing
			commands[index].DuplicateMetadata = option.DuplicateMetadata
		}
	}
	return s.urlImports.EnqueueURLs(ctx, commands, input.BaseID, input.RequestID)
}

func (s *ToolServer) libraryImportStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		TaskID string `json:"task_id"`
	}
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if s.urlImports == nil {
		return nil, types.ErrTaskInspectorUnavailable
	}
	return s.urlImports.GetURLImportTask(ctx, strings.TrimSpace(input.TaskID))
}

type importFilesInput struct {
	Options []types.DocumentImportOptions `json:"options"`
	Paths   []string                      `json:"paths"`
	DOIs    []string                      `json:"dois"`
	BaseID  string                        `json:"base_id"`
}

func (s *ToolServer) libraryImportFiles(ctx context.Context, raw json.RawMessage) (any, error) {
	var input importFilesInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := validateImportBatch(len(input.Paths), len(input.DOIs), "paths"); err != nil {
		return nil, err
	}
	if err := validateMCPImportOptions(raw, len(input.Paths), input.Options); err != nil {
		return nil, err
	}
	paths := make([]string, len(input.Paths))
	for index, rawPath := range input.Paths {
		path, err := s.validateLocalImportFile(rawPath)
		if err != nil {
			return nil, fmt.Errorf("path %d: %w", index+1, err)
		}
		paths[index] = path
	}
	if err := s.validateImportBase(ctx, input.BaseID); err != nil {
		return nil, err
	}
	output := libraryImportBatchResult{BaseID: strings.TrimSpace(input.BaseID),
		DocumentBatchResult: types.DocumentBatchResult{Items: []types.DocumentBatchItemResult{}}}
	for index, path := range paths {
		if ctx.Err() != nil {
			output.Stopped = true
			for next := index; next < len(paths); next++ {
				output.Items = append(output.Items, types.DocumentBatchItemResult{
					Index: next, Filename: filepath.Base(paths[next]),
					Status: types.DocumentImportItemFailed, Error: "batch stopped before this file was imported",
				})
			}
			break
		}
		var dois []string
		if len(input.DOIs) > 0 {
			dois = []string{input.DOIs[index]}
		}
		var options [][]types.DocumentImportOptions
		if len(input.Options) > 0 {
			options = [][]types.DocumentImportOptions{{input.Options[index]}}
		}
		result, err := s.documents.CreateDocumentsFromLocalFiles(ctx, []string{path}, dois, options...)
		if err != nil {
			return nil, err
		}
		attached := s.attachImportBatchToBase(ctx, input.BaseID, result)
		for _, item := range attached.Items {
			item.Index = index
			output.Items = append(output.Items, item)
		}
		if attached.Error != "" {
			output.Error = attached.Error
		}
		if attached.Stopped {
			output.Stopped = true
			for next := index + 1; next < len(paths); next++ {
				output.Items = append(output.Items, types.DocumentBatchItemResult{
					Index: next, Filename: filepath.Base(paths[next]),
					Status: types.DocumentImportItemFailed, Error: "batch stopped before this file was imported",
				})
			}
			break
		}
	}
	output.DocumentBatchResult.UpdateSummary()
	return output, nil
}

// validateLocalImportFile confines one path to the configured root and applies
// the same type, regular-file, and size checks as the service.
func (s *ToolServer) validateLocalImportFile(raw string) (string, error) {
	path, err := s.resolveLocalImportPath(raw)
	if err != nil {
		return "", err
	}
	if _, supported := fileservice.SupportedContentType(path); !supported {
		return "", fmt.Errorf("%w: unsupported local file type", errInvalidToolInput)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: local file is not readable", errInvalidToolInput)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: path must name a regular file", errInvalidToolInput)
	}
	maxSize := s.cfg.MaxUploadSize
	if maxSize <= 0 {
		maxSize = fileservice.DefaultMaxUploadSize
	}
	if info.Size() > maxSize {
		return "", fmt.Errorf("%w: local file exceeds the upload limit", errPayloadTooLarge)
	}
	return path, nil
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
			DocumentWorkflowSummary: document.WorkflowSummary(),
			DocumentID:              document.ID, SourceState: document.SourceState,
			CanonicalDocumentID: document.CanonicalDocumentID, StageStates: states,
		}}, nil
	}
	view, err := s.processing.GetView(ctx, *document)
	if err != nil {
		return nil, err
	}
	view.DocumentWorkflowSummary = document.WorkflowSummary()
	if progress, progressErr := s.documents.GetReferenceProgress(ctx, document.ID.String()); progressErr == nil {
		view.ReferenceProgress = &progress
	}
	return map[string]any{"processing": view}, nil
}

type libraryGetManyInput struct {
	DocumentIDs       []string `json:"document_ids"`
	IncludeProcessing bool     `json:"include_processing"`
}

type libraryGetManyItem struct {
	Document      types.Document                `json:"document"`
	PaperMetadata *types.PaperMetadata          `json:"paperMetadata,omitempty"`
	Processing    *types.DocumentProcessingView `json:"processing,omitempty"`
}

type libraryGetManyResult struct {
	Items              []libraryGetManyItem `json:"items"`
	MissingDocumentIDs []string             `json:"missingDocumentIds,omitempty"`
}

// validateDocumentIDBatch normalizes, deduplicates, and bounds one batch read
// request. Duplicate IDs collapse to one result; missing IDs are reported by
// the callers, not treated as an error.
func validateDocumentIDBatch(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > types.MaxDocumentProjectionIDs {
		return nil, fmt.Errorf(
			"%w: document_ids must contain between 1 and %d ids",
			errInvalidToolInput, types.MaxDocumentProjectionIDs,
		)
	}
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("%w: invalid document id", errInvalidToolInput)
		}
		canonical := id.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

func (s *ToolServer) loadDocumentProjections(
	ctx context.Context, ids []string, includeMetadata, includeProcessing bool,
) (map[uuid.UUID]types.DocumentProjection, []string, error) {
	if s.documents == nil {
		return nil, nil, fmt.Errorf("document service is unavailable")
	}
	projections, err := s.documents.GetDocumentProjections(ctx, types.DocumentProjectionQuery{
		DocumentIDs: ids, IncludeMetadata: includeMetadata, IncludeProcessing: includeProcessing,
	})
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[uuid.UUID]types.DocumentProjection, len(projections))
	for _, projection := range projections {
		byID[projection.Document.ID] = projection
	}
	missing := make([]string, 0)
	for _, raw := range ids {
		id, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			continue
		}
		if _, found := byID[id]; !found {
			missing = append(missing, raw)
		}
	}
	return byID, missing, nil
}

func (s *ToolServer) libraryGetMany(ctx context.Context, raw json.RawMessage) (any, error) {
	var input libraryGetManyInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	ids, err := validateDocumentIDBatch(input.DocumentIDs)
	if err != nil {
		return nil, err
	}
	byID, missing, err := s.loadDocumentProjections(ctx, ids, true, input.IncludeProcessing)
	if err != nil {
		return nil, err
	}
	items := make([]libraryGetManyItem, 0, len(ids)-len(missing))
	for _, id := range ids {
		projection, found := byID[uuid.MustParse(id)]
		if !found {
			continue
		}
		items = append(items, libraryGetManyItem{
			Document: projection.Document, PaperMetadata: projection.Metadata,
			Processing: projection.Processing,
		})
	}
	return libraryGetManyResult{Items: items, MissingDocumentIDs: missing}, nil
}

type processingStatusManyInput struct {
	DocumentIDs []string `json:"document_ids"`
}

type processingStatusManyResult struct {
	Status             string                         `json:"status"`
	Summary            map[string]int                 `json:"summary"`
	Items              []types.DocumentProcessingView `json:"items"`
	MissingDocumentIDs []string                       `json:"missingDocumentIds,omitempty"`
}

func (s *ToolServer) processingStatusMany(ctx context.Context, raw json.RawMessage) (any, error) {
	var input processingStatusManyInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	ids, err := validateDocumentIDBatch(input.DocumentIDs)
	if err != nil {
		return nil, err
	}
	byID, missing, err := s.loadDocumentProjections(ctx, ids, false, true)
	if err != nil {
		return nil, err
	}
	items := make([]types.DocumentProcessingView, 0, len(ids)-len(missing))
	for _, id := range ids {
		projection, found := byID[uuid.MustParse(id)]
		if !found {
			continue
		}
		view := types.DocumentProcessingView{DocumentID: projection.Document.ID}
		if projection.Processing != nil {
			view = *projection.Processing
		} else {
			view.Stages = []types.DocumentProcessingSpan{}
			for _, stage := range []types.DocumentStage{
				types.DocumentStageMarkdown, types.DocumentStageMetadataExtraction,
				types.DocumentStageSourceLookup, types.DocumentStageReferenceLookup,
			} {
				state, _ := projection.Document.StageState(stage)
				view.StageStates = append(view.StageStates, state)
			}
		}
		view.DocumentWorkflowSummary = projection.Document.WorkflowSummary()
		items = append(items, view)
	}
	summary := map[string]int{"total": len(ids), "readingReady": 0, "processing": 0,
		"waiting": 0, "succeeded": 0, "partial": 0, "failed": len(missing), "missing": len(missing)}
	for _, item := range items {
		summary[item.WorkflowStatus]++
		if item.ReadingReady {
			summary["readingReady"]++
		}
	}
	status := "succeeded"
	switch {
	case summary["processing"] > 0:
		status = "processing"
	case summary["failed"] == len(ids):
		status = "failed"
	case summary["failed"] > 0 || summary["partial"] > 0:
		status = "partial"
	case summary["waiting"] > 0:
		status = "waiting"
	}
	return processingStatusManyResult{
		Items: items, MissingDocumentIDs: missing, Status: status, Summary: summary,
	}, nil
}
