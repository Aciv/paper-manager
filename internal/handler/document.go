// Package handle contains HTTP endpoint handlers.
package handler

import (
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
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

type DocumentHandler struct {
	documents     interfaces.DocumentService
	maxUploadSize int64
}

type updateDocumentRequest struct {
	OriginalFilename *string `json:"originalFilename"`
	// Filename is a backwards-compatible input alias. API responses use the
	// clearer originalFilename name and never use this deprecated field.
	Filename *string `json:"filename"`
	DOI      *string `json:"doi"`
}

// NewDocumentHandler receives the application service through DI; handlers do
// not coordinate storage or repositories directly.
func NewDocumentHandler(cfg config.Config, documents interfaces.DocumentService) *DocumentHandler {
	maxSize := cfg.MaxUploadSize
	if maxSize <= 0 {
		maxSize = fileservice.DefaultMaxUploadSize
	}
	return &DocumentHandler{documents: documents, maxUploadSize: maxSize}
}

// Upload accepts multipart/form-data with a required `file` field.
func (h *DocumentHandler) Upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUploadSize+1024*1024)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.writeError(c, apperrors.NewPayloadTooLargeError("file exceeds the configured upload limit"), err)
			return
		}
		h.writeError(c, apperrors.NewBadRequestError("multipart field 'file' is required"), err)
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
		h.writeUploadError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"document": *created})
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
	if rawStatus := strings.TrimSpace(c.Query("status")); rawStatus != "" {
		search.Status = types.DocumentStatus(rawStatus)
	}
	result, err := h.documents.SearchDocument(c.Request.Context(), search)
	if err != nil {
		h.writeError(c, apperrors.NewInternalServerError("search documents"), err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// Content streams an original document for in-browser viewing. It is kept in
// ServerFile with upload/delete because the service owns storage authorization
// and never exposes provider paths to the browser.
func (h *DocumentHandler) Content(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	document, err := h.documents.GetDocumentByID(c.Request.Context(), documentID.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("load document metadata"), err)
		return
	}
	reader, filename, err := h.documents.GetDocumentFile(c.Request.Context(), documentID.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("open document content"), err)
		return
	}
	defer reader.Close()
	c.DataFromReader(http.StatusOK, document.SizeBytes, document.ContentType, reader, map[string]string{
		"Content-Disposition":    mime.FormatMediaType("inline", map[string]string{"filename": filename}),
		"X-Content-Type-Options": "nosniff",
	})
}

// ListBlocks returns source-map blocks for the requested PDF page. A missing
// page query loads all blocks, which is useful to a Markdown-side navigator;
// the PDF reader normally requests one page at a time.
func (h *DocumentHandler) ListBlocks(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	pageNumber := 0
	if rawPage := strings.TrimSpace(c.Query("page")); rawPage != "" {
		pageNumber, err = strconv.Atoi(rawPage)
		if err != nil || pageNumber < 1 || pageNumber > 100000 {
			h.writeError(c, apperrors.NewBadRequestError("page must be between 1 and 100000"), err)
			return
		}
	}
	blocks, err := h.documents.GetDocumentBlocks(c.Request.Context(), documentID.String(), pageNumber)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("load document blocks"), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": blocks})
}

// BlockImage streams one rendered image/table crop. The database holds a
// private storage key, while blocks expose this protected endpoint as URL.
func (h *DocumentHandler) BlockImage(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	blockID, err := uuid.Parse(c.Param("blockID"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid block id"), err)
		return
	}
	reader, filename, err := h.documents.GetDocumentBlockImage(c.Request.Context(), documentID.String(), blockID.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document block image not found"), nil)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("open document block image"), err)
		return
	}
	defer reader.Close()
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.DataFromReader(http.StatusOK, -1, contentType, reader, map[string]string{
		"Content-Disposition":    mime.FormatMediaType("inline", map[string]string{"filename": filename}),
		"X-Content-Type-Options": "nosniff",
	})
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
