package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"path/filepath"
	"strings"
	"unicode/utf8"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
)

// CreateDocumentsFromFiles accepts every supplied multipart item separately.
// Sequential acceptance keeps one request from creating an unbounded burst of
// storage writes and queue jobs; document recognition remains asynchronous.
func (s *documentService) CreateDocumentsFromFiles(
	ctx context.Context, files []*multipart.FileHeader, dois []string, options ...[]types.DocumentImportOptions,
) (result types.DocumentBatchResult, outputErr error) {
	defer func() { result.UpdateSummary() }()
	if err := validateBatchLength(len(files)); err != nil {
		return types.DocumentBatchResult{}, err
	}
	if len(dois) != 0 && len(dois) != len(files) {
		return types.DocumentBatchResult{}, errors.New("each batch file requires at most one matching doi")
	}

	if err := validateImportOptionsBatch(len(files), options); err != nil {
		return types.DocumentBatchResult{}, err
	}
	result = types.DocumentBatchResult{Items: make([]types.DocumentBatchItemResult, 0, len(files))}
	for index, file := range files {
		doi := ""
		if len(dois) != 0 {
			doi = strings.TrimSpace(dois[index])
		}
		item := types.DocumentBatchItemResult{Index: index}
		if file != nil {
			item.Filename = file.Filename
		}
		if !validBatchDOI(doi) {
			item.Status = types.DocumentImportItemRejected
			item.Error = "doi must be at most 255 characters and contain no control characters"
			result.Items = append(result.Items, item)
			continue
		}
		if file == nil {
			item.Status = types.DocumentImportItemRejected
			item.Error = "file is required"
			result.Items = append(result.Items, item)
			continue
		}
		created, err := s.CreateDocumentFromFile(ctx, file, doi, batchImportOptions(options, index)...)
		if errors.Is(err, errDocumentDuplicateLookup) {
			return stopBatchForDeduplicationFailure(ctx, result, item, err), nil
		}
		result.Items = append(result.Items, batchResultForDocument(item, created, err))
	}
	return result, nil
}

// CreateDocumentsFromURLs shares CreateDocumentFromURL's resolver, SSRF
// validation, download limits, hash deduplication, persistence, and queueing.
func (s *documentService) CreateDocumentsFromURLs(
	ctx context.Context, commands []types.DocumentURLImportCommand,
) (result types.DocumentBatchResult, outputErr error) {
	defer func() { result.UpdateSummary() }()
	if err := validateBatchLength(len(commands)); err != nil {
		return types.DocumentBatchResult{}, err
	}
	encoded, err := json.Marshal(commands)
	if err != nil || len(encoded) > types.MaxDocumentImportPayloadBytes {
		return types.DocumentBatchResult{}, invalidImport("URL import payload exceeds 1 MiB")
	}
	for _, command := range commands {
		if _, err := command.Processing.SourceLookup.Normalized(); err != nil {
			return types.DocumentBatchResult{}, err
		}
		if _, err := command.DuplicateMetadata.Normalized(); err != nil {
			return types.DocumentBatchResult{}, err
		}
	}
	result = types.DocumentBatchResult{Items: make([]types.DocumentBatchItemResult, 0, len(commands))}
	for index, command := range commands {
		if ctx.Err() != nil {
			return stopBatchForContext(ctx, result, index, commands), nil
		}
		item := types.DocumentBatchItemResult{Index: index, URL: strings.TrimSpace(command.URL)}
		created, err := s.CreateDocumentFromURL(ctx, command)
		if errors.Is(err, errDocumentDuplicateLookup) {
			return stopBatchForDeduplicationFailure(ctx, result, item, err), nil
		}
		result.Items = append(result.Items, batchResultForDocument(item, created, err))
		if ctx.Err() != nil {
			return stopBatchForContext(ctx, result, index+1, commands), nil
		}
	}
	return result, nil
}

// CreateDocumentsFromLocalFiles independently accepts a bounded batch of local
// files whose paths the caller has already contained and validated. It applies
// the same file, size, DOI, and duplicate rules as a single local import.
func (s *documentService) CreateDocumentsFromLocalFiles(
	ctx context.Context, paths []string, dois []string, options ...[]types.DocumentImportOptions,
) (result types.DocumentBatchResult, outputErr error) {
	defer func() { result.UpdateSummary() }()
	if err := validateBatchLength(len(paths)); err != nil {
		return types.DocumentBatchResult{}, err
	}
	if len(dois) != 0 && len(dois) != len(paths) {
		return types.DocumentBatchResult{}, errors.New("each batch file requires at most one matching doi")
	}

	if err := validateImportOptionsBatch(len(paths), options); err != nil {
		return types.DocumentBatchResult{}, err
	}
	result = types.DocumentBatchResult{Items: make([]types.DocumentBatchItemResult, 0, len(paths))}
	for index, path := range paths {
		if ctx.Err() != nil {
			return stopLocalBatchForContext(ctx, result, index, paths), nil
		}
		path = strings.TrimSpace(path)
		doi := ""
		if len(dois) != 0 {
			doi = strings.TrimSpace(dois[index])
		}
		item := types.DocumentBatchItemResult{Index: index}
		if path != "" {
			item.Filename = filepath.Base(path)
		}
		if !validBatchDOI(doi) {
			item.Status = types.DocumentImportItemRejected
			item.Error = "doi must be at most 255 characters and contain no control characters"
			result.Items = append(result.Items, item)
			continue
		}
		if path == "" {
			item.Status = types.DocumentImportItemRejected
			item.Error = "path is required"
			result.Items = append(result.Items, item)
			continue
		}
		created, err := s.CreateDocumentFromLocalFile(ctx, path, doi, batchImportOptions(options, index)...)
		if errors.Is(err, errDocumentDuplicateLookup) {
			return stopBatchForDeduplicationFailure(ctx, result, item, err), nil
		}
		result.Items = append(result.Items, batchResultForDocument(item, created, err))
	}
	return result, nil
}

// stopBatchForContext preserves completed items and marks every unprocessed
// URL failed. The handler returns the partial result within its deadline
// instead of holding the HTTP connection for the full per-item budget.
func stopBatchForContext(
	ctx context.Context,
	result types.DocumentBatchResult,
	nextIndex int,
	commands []types.DocumentURLImportCommand,
) types.DocumentBatchResult {
	result.Stopped = true
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Error = "batch import deadline exceeded"
	} else {
		result.Error = "batch import cancelled"
	}
	for index := nextIndex; index < len(commands); index++ {
		result.Items = append(result.Items, types.DocumentBatchItemResult{
			Index: index, URL: strings.TrimSpace(commands[index].URL),
			Status: types.DocumentImportItemFailed,
			Error:  "batch import stopped before this item",
		})
	}
	return result
}

// stopLocalBatchForContext mirrors stopBatchForContext for local paths.
func stopLocalBatchForContext(
	ctx context.Context,
	result types.DocumentBatchResult,
	nextIndex int,
	paths []string,
) types.DocumentBatchResult {
	result.Stopped = true
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Error = "batch import deadline exceeded"
	} else {
		result.Error = "batch import cancelled"
	}
	for index := nextIndex; index < len(paths); index++ {
		filename := ""
		if path := strings.TrimSpace(paths[index]); path != "" {
			filename = filepath.Base(path)
		}
		result.Items = append(result.Items, types.DocumentBatchItemResult{
			Index: index, Filename: filename,
			Status: types.DocumentImportItemFailed,
			Error:  "batch import stopped before this item",
		})
	}
	return result
}

func validateBatchLength(length int) error {
	if length == 0 {
		return errors.New("provide at least one batch item")
	}
	if length > types.MaxDocumentBatchItems {
		return fmt.Errorf("a batch may contain at most %d items", types.MaxDocumentBatchItems)
	}
	return nil
}

func validBatchDOI(doi string) bool {
	return utf8.ValidString(doi) && utf8.RuneCountInString(doi) <= maxDocumentDOILength && !strings.ContainsAny(doi, "\r\n\t")
}

func batchResultForDocument(
	item types.DocumentBatchItemResult, document *types.Document, err error,
) types.DocumentBatchItemResult {
	if document != nil {
		item.ImportMetadata = document.ImportMetadata
	}
	if err == nil && document != nil {
		queued := true
		item.Status, item.Document, item.ProcessingQueued = types.DocumentImportItemAccepted, document, &queued
		return item
	}
	if errors.Is(err, ErrDocumentProcessQueue) && document != nil {
		queued, retryable := false, true
		item.Status, item.Document = types.DocumentImportItemAccepted, document
		item.ProcessingQueued, item.Retryable = &queued, &retryable
		return item
	}
	if (errors.Is(err, ErrDuplicateDOI) || errors.Is(err, types.ErrDuplicateFileHash)) && document != nil {
		item.Status, item.Document = types.DocumentImportItemDuplicate, document
		return item
	}
	item.Status, item.Error = batchFailure(err)
	return item
}

func batchFailure(err error) (types.DocumentImportItemStatus, string) {
	switch {
	case errors.Is(err, types.ErrInvalidDocumentImport),
		errors.Is(err, types.ErrInvalidRemotePDFRequest),
		errors.Is(err, types.ErrRemotePDFUnavailable),
		errors.Is(err, ErrInvalidDocumentFilename),
		errors.Is(err, fileservice.ErrFileTooLarge),
		errors.Is(err, fileservice.ErrInvalidPDF),
		errors.Is(err, fileservice.ErrEmptyFile),
		errors.Is(err, fileservice.ErrUnsupportedFileType):
		return types.DocumentImportItemRejected, "document input is invalid or unavailable"
	default:
		return types.DocumentImportItemFailed, "document could not be accepted"
	}
}

// stopBatchForDeduplicationFailure preserves durable outcomes already sent to
// storage and the database, but refuses to create later documents while the
// repository cannot establish whether their DOI or content hash is unique.
func stopBatchForDeduplicationFailure(
	ctx context.Context,
	result types.DocumentBatchResult,
	item types.DocumentBatchItemResult,
	err error,
) types.DocumentBatchResult {
	logger.Error(ctx, "batch document import stopped because duplicate lookup failed", "error", err)
	item.Status = types.DocumentImportItemFailed
	item.Error = "duplicate detection is temporarily unavailable"
	result.Items = append(result.Items, item)
	result.Stopped = true
	result.Error = "batch stopped because duplicate detection is temporarily unavailable"
	return result
}
