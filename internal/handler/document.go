// Package handle contains HTTP endpoint handlers.
package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	appservice "paperMaster/internal/application/service"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxDOILength = 255

const (
	documentBatchMultipartOverhead int64 = 1 << 20
	maxDocumentBatchRequestSize    int64 = 500 << 20
)

type DocumentHandler struct {
	documents          interfaces.DocumentService
	urlImports         interfaces.URLImportService
	maxUploadSize      int64
	batchImportTimeout time.Duration
}

func NewDocumentHandlerWithURLImports(
	cfg config.Config, documents interfaces.DocumentService, imports interfaces.URLImportService,
) *DocumentHandler {
	h := NewDocumentHandler(cfg, documents)
	h.urlImports = imports
	return h
}

type updateDocumentRequest struct {
	OriginalFilename *string `json:"originalFilename"`
	// Filename is a backwards-compatible input alias. API responses use the
	// clearer originalFilename name and never use this deprecated field.
	Filename *string `json:"filename"`
	DOI      *string `json:"doi"`
}

// ImportURL accepts a trusted provider landing page or an explicit PDF URL.
// The service owns URL resolution, SSRF checks, streaming, and persistence.
func (h *DocumentHandler) ImportURL(c *gin.Context) {
	var command types.DocumentURLImportCommand
	if err := c.ShouldBindJSON(&command); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	created, err := h.documents.CreateDocumentFromURL(c.Request.Context(), command)
	if err != nil {
		if created != nil && errors.Is(err, appservice.ErrDocumentProcessQueue) {
			h.writeCreatedButNotQueued(c, *created)
			return
		}
		switch {
		case errors.Is(err, types.ErrInvalidRemotePDFRequest):
			h.writeError(c, apperrors.NewBadRequestError("URL and DOI must be valid"), err)
		case errors.Is(err, types.ErrRemotePDFUnavailable):
			h.writeError(c, apperrors.NewBadRequestError("URL does not resolve to a supported PDF"), err)
		case errors.Is(err, appservice.ErrDuplicateDOI), errors.Is(err, appservice.ErrDuplicateFileHash):
			h.writeError(c, apperrors.NewConflictError("document already exists"), err)
		case errors.Is(err, appservice.ErrInvalidDocumentFilename):
			h.writeError(c, apperrors.NewBadRequestError("filename must be a safe PDF filename"), err)
		case errors.Is(err, fileservice.ErrFileTooLarge):
			h.writeError(c, apperrors.NewPayloadTooLargeError("remote PDF exceeds the configured upload limit"), err)
		case errors.Is(err, fileservice.ErrInvalidPDF):
			h.writeError(c, apperrors.NewBadRequestError("remote content is not a valid PDF"), err)
		default:
			h.writeError(c, apperrors.NewInternalServerError("import remote document"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "remote document import accepted", "document_id", created.ID)
	c.JSON(http.StatusCreated, gin.H{"document": created})
}

// ImportURLBatch accepts a bounded list of explicit remote PDF imports. It
// deliberately returns one outcome per URL so a bad provider response does
// not discard the rest of the user's batch.
func (h *DocumentHandler) ImportURLBatch(c *gin.Context) {
	var request types.DocumentURLImportBatchRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	if len(request.URLs) == 0 || len(request.URLs) > types.MaxDocumentBatchItems {
		h.writeError(c, apperrors.NewBadRequestError("provide between 1 and 20 URLs"), nil)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.batchImportTimeout)
	defer cancel()
	result, err := h.documents.CreateDocumentsFromURLs(ctx, request.URLs)
	if err != nil {
		h.writeError(c, apperrors.NewInternalServerError("import remote document batch"), err)
		return
	}
	if result.Stopped {
		logger.Warn(c.Request.Context(), "remote document batch import stopped", "item_count", len(result.Items))
	} else {
		logger.Info(c.Request.Context(), "remote document batch import accepted", "item_count", len(result.Items))
	}
	c.JSON(http.StatusOK, result)
}

// DownloadReferencePDF explicitly requests acquisition of one shared
// reference. Reference parsing itself never triggers this operation.
func (h *DocumentHandler) DownloadReferencePDF(c *gin.Context) {
	referenceID := c.Param("referenceID")
	if err := h.documents.RequestReferencePDFDownload(c.Request.Context(), referenceID); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			h.writeError(c, apperrors.NewNotFoundError("reference not found"), nil)
		case errors.Is(err, types.ErrRemotePDFUnavailable):
			h.writeError(c, apperrors.NewBadRequestError("reference has no downloadable source"), err)
		case errors.Is(err, appservice.ErrReferenceAlreadyResolved):
			h.writeError(c, apperrors.NewConflictError("reference already has a source document"), err)
		case errors.Is(err, types.ErrReferenceDownloadInProgress):
			h.writeError(c, apperrors.NewConflictError("reference download is already in progress"), err)
		case strings.Contains(err.Error(), "invalid reference id"):
			h.writeError(c, apperrors.NewBadRequestError("invalid reference id"), err)
		default:
			h.writeError(c, apperrors.NewInternalServerError("queue reference PDF download"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "reference PDF download accepted", "reference_id", referenceID)
	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

// NewDocumentHandler receives the application service through DI; handlers do
// not coordinate storage or repositories directly.
func NewDocumentHandler(cfg config.Config, documents interfaces.DocumentService) *DocumentHandler {
	maxSize := cfg.MaxUploadSize
	if maxSize <= 0 {
		maxSize = fileservice.DefaultMaxUploadSize
	}
	timeout := cfg.BatchImportTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &DocumentHandler{documents: documents, maxUploadSize: maxSize, batchImportTimeout: timeout}
}

// Upload accepts multipart/form-data with a required `file` field.
func (h *DocumentHandler) Upload(c *gin.Context) {
	if c.Request.ContentLength > h.maxUploadSize+1024*1024 {
		h.writeError(c, apperrors.NewPayloadTooLargeError("file exceeds the configured upload limit"), nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUploadSize+1024*1024)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		logger.Warn(c.Request.Context(), "multipart upload parse failed",
			"content_length", c.Request.ContentLength,
			"content_type", c.Request.Header.Get("Content-Type"),
			"max_upload_size", h.maxUploadSize,
			"error", err)
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.writeError(c, apperrors.NewPayloadTooLargeError("file exceeds the configured upload limit"), err)
			return
		}
		h.writeError(c, apperrors.NewBadRequestError("multipart field 'file' is required"), err)
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if _, supported := fileservice.SupportedContentType(fileHeader.Filename); !supported {
		h.writeError(c, apperrors.NewUnsupportedMediaTypeError("unsupported file type"), nil)
		return
	}
	if fileHeader.Size > h.maxUploadSize {
		h.writeError(c, apperrors.NewPayloadTooLargeError("file exceeds the configured upload limit"), nil)
		return
	}
	doi := strings.TrimSpace(c.PostForm("doi"))
	if !utf8.ValidString(doi) || utf8.RuneCountInString(doi) > maxDOILength || strings.ContainsAny(doi, "\r\n\t") {
		h.writeError(c, apperrors.NewBadRequestError("doi must be at most 255 characters and contain no control characters"), nil)
		return
	}
	created, err := h.documents.CreateDocumentFromFile(c.Request.Context(), fileHeader, doi)
	if err != nil {
		if created != nil && errors.Is(err, appservice.ErrDocumentProcessQueue) {
			h.writeCreatedButNotQueued(c, *created)
			return
		}
		h.writeUploadError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"document": *created})
}

// UploadBatch accepts multipart/form-data containing repeated `files` fields.
// An optional repeated `doi` field maps by index to its file; callers must
// provide either no DOI fields or exactly one for every file.
func (h *DocumentHandler) UploadBatch(c *gin.Context) {
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		h.writeError(c, apperrors.NewUnsupportedMediaTypeError("batch upload must use multipart/form-data"), nil)
		return
	}
	batchLimit := batchUploadRequestSize(h.maxUploadSize)
	if c.Request.ContentLength > batchLimit {
		h.writeError(c, apperrors.NewPayloadTooLargeError("batch upload exceeds the configured batch limit"), nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, batchLimit)
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		logger.Warn(c.Request.Context(), "batch multipart parse failed",
			"content_length", c.Request.ContentLength,
			"content_type", c.Request.Header.Get("Content-Type"),
			"batch_limit", batchLimit,
			"error", err)
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.writeError(c, apperrors.NewPayloadTooLargeError("batch upload exceeds the configured batch limit"), err)
			return
		}
		h.writeError(c, apperrors.NewBadRequestError("invalid multipart batch upload"), err)
		return
	}
	if c.Request.MultipartForm == nil {
		h.writeError(c, apperrors.NewBadRequestError("multipart field 'files' is required"), nil)
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	files := c.Request.MultipartForm.File["files"]
	dois := c.Request.MultipartForm.Value["doi"]
	if len(files) == 0 || len(files) > types.MaxDocumentBatchItems {
		h.writeError(c, apperrors.NewBadRequestError("provide between 1 and 20 files"), nil)
		return
	}
	if len(dois) != 0 && len(dois) != len(files) {
		h.writeError(c, apperrors.NewBadRequestError("provide either no DOI values or one DOI for each file"), nil)
		return
	}
	for _, doi := range dois {
		if !validDocumentDOI(doi) {
			h.writeError(c, apperrors.NewBadRequestError("doi must be at most 255 characters and contain no control characters"), nil)
			return
		}
	}
	// Reject an oversized or unsupported item before the service hashes any
	// file in the batch. Remaining valid items keep the batch usable.
	for _, file := range files {
		if _, supported := fileservice.SupportedContentType(file.Filename); !supported {
			h.writeError(c, apperrors.NewUnsupportedMediaTypeError("unsupported file type"), nil)
			return
		}
		if file.Size > h.maxUploadSize {
			h.writeError(c, apperrors.NewPayloadTooLargeError("file exceeds the configured upload limit"), nil)
			return
		}
	}
	result, err := h.documents.CreateDocumentsFromFiles(c.Request.Context(), files, dois)
	if err != nil {
		h.writeError(c, apperrors.NewInternalServerError("create document batch"), err)
		return
	}
	if result.Stopped {
		logger.Warn(c.Request.Context(), "document batch upload stopped", "item_count", len(result.Items))
	} else {
		logger.Info(c.Request.Context(), "document batch upload accepted", "item_count", len(result.Items))
	}
	c.JSON(http.StatusOK, result)
}

func validDocumentDOI(doi string) bool {
	doi = strings.TrimSpace(doi)
	return utf8.ValidString(doi) && utf8.RuneCountInString(doi) <= maxDOILength && !strings.ContainsAny(doi, "\r\n\t")
}

func batchUploadRequestSize(maxFileSize int64) int64 {
	if maxFileSize <= 0 {
		maxFileSize = fileservice.DefaultMaxUploadSize
	}
	if maxFileSize > (maxDocumentBatchRequestSize-documentBatchMultipartOverhead)/int64(types.MaxDocumentBatchItems) {
		return maxDocumentBatchRequestSize
	}
	return maxFileSize*int64(types.MaxDocumentBatchItems) + documentBatchMultipartOverhead
}

// writeCreatedButNotQueued reports a durable upload whose initial background
// task failed to enqueue. This remains a successful resource creation, not a
// 5xx: returning its ID lets the client call the normal retry endpoint.
func (h *DocumentHandler) writeCreatedButNotQueued(c *gin.Context, document types.Document) {
	logger.Warn(c.Request.Context(), "document created but processing was not queued",
		"document_id", document.ID, "markdown_status", document.MarkdownStatus)
	c.JSON(http.StatusCreated, gin.H{
		"document":         document,
		"processingQueued": false,
		"retryable":        true,
	})
}

// DeleteFile removes the stored document file and its database metadata.
func (h *DocumentHandler) DeleteFile(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	_, err = h.documents.GetDocumentByID(c.Request.Context(), documentID.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("load document metadata"), err)
		return
	}
	if err := h.documents.DeleteDocument(c.Request.Context(), documentID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("delete document metadata"), err)
		return
	}
	logger.Info(c.Request.Context(), "document deletion completed", "document_id", documentID)
	c.Status(http.StatusNoContent)
}

// UpdateDocument changes source-file fields only. Bibliographic fields are
// handled by PaperMetadataHandler.
func (h *DocumentHandler) UpdateDocument(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	var request updateDocumentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	if request.OriginalFilename == nil {
		request.OriginalFilename = request.Filename
	}
	if request.OriginalFilename == nil && request.DOI == nil {
		h.writeError(c, apperrors.NewBadRequestError("provide originalFilename and/or doi"), nil)
		return
	}
	updated, err := h.documents.UpdateDocumentInfo(c.Request.Context(), documentID.String(), request.OriginalFilename, request.DOI)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
		case errors.Is(err, appservice.ErrDuplicateDOI):
			h.writeError(c, apperrors.NewConflictError("doi already exists"), err)
		case errors.Is(err, appservice.ErrInvalidDocumentFilename):
			h.writeError(c, apperrors.NewBadRequestError("filename must be a safe file name"), err)
		case errors.Is(err, appservice.ErrInvalidDocumentDOI):
			h.writeError(c, apperrors.NewBadRequestError("doi must be at most 255 characters and contain no control characters"), err)
		default:
			h.writeError(c, apperrors.NewInternalServerError("update document metadata"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "document metadata update completed", "document_id", updated.ID)
	c.JSON(http.StatusOK, gin.H{"document": updated})
}

// Search returns documents by exact ID, DOI, or all source documents.
func (h *DocumentHandler) Search(c *gin.Context) {
	if strings.TrimSpace(c.Query("title")) != "" || len(c.QueryArray("tag")) > 0 {
		h.writeError(c, apperrors.NewBadRequestError("document search only supports id, doi, and status"), nil)
		return
	}
	search := types.DocumentSearch{
		DOI:      strings.TrimSpace(c.Query("doi")),
		Page:     1,
		PageSize: types.DocumentPageSize,
	}
	if rawPage := c.Query("page"); rawPage != "" {
		page, err := strconv.Atoi(rawPage)
		if err != nil || page < 1 || page > types.MaxDocumentSearchPage {
			h.writeError(c, apperrors.NewBadRequestError("page must be between 1 and 10000"), err)
			return
		}
		search.Page = page
	}
	if rawID := strings.TrimSpace(c.Query("id")); rawID != "" {
		id, err := uuid.Parse(rawID)
		if err != nil {
			h.writeError(c, apperrors.NewBadRequestError("id must be a UUID"), err)
			return
		}
		search.ID = &id
	}
	if rawSourceState := strings.TrimSpace(c.Query("sourceState")); rawSourceState != "" {
		search.SourceState = types.DocumentSourceState(rawSourceState)
	}
	if rawExcludeUnknown := strings.TrimSpace(c.Query("excludeUnknown")); rawExcludeUnknown != "" {
		excludeUnknown, err := strconv.ParseBool(rawExcludeUnknown)
		if err != nil {
			h.writeError(c, apperrors.NewBadRequestError("excludeUnknown must be true or false"), err)
			return
		}
		search.ExcludeUnknown = excludeUnknown
	}
	result, err := h.documents.SearchDocument(c.Request.Context(), search)
	if err != nil {
		h.writeError(c, apperrors.NewInternalServerError("search documents"), err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *DocumentHandler) writeUploadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, appservice.ErrDuplicateDOI):
		h.writeError(c, apperrors.NewConflictError("doi already exists"), err)
	case errors.Is(err, appservice.ErrDuplicateFileHash):
		h.writeError(c, apperrors.NewConflictError("an identical file already exists"), err)
	case errors.Is(err, fileservice.ErrFileTooLarge):
		h.writeError(c, apperrors.NewPayloadTooLargeError("file exceeds the configured upload limit"), err)
	case errors.Is(err, fileservice.ErrUnsupportedFileType):
		h.writeError(c, apperrors.NewUnsupportedMediaTypeError("unsupported file type"), err)
	case errors.Is(err, fileservice.ErrEmptyFile), errors.Is(err, fileservice.ErrInvalidPDF), errors.Is(err, fileservice.ErrInvalidOfficeFile):
		h.writeError(c, apperrors.NewBadRequestError(err.Error()), err)
	default:
		h.writeError(c, apperrors.NewInternalServerError("store uploaded file"), err)
	}
}

// writeError delegates API error serialization to middleware.ErrorHandler.
// The original cause is logged but never returned to the client.
func (h *DocumentHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "document request failed", serverErr, cause)
}
