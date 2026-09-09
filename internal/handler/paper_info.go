package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
)

// PaperInfoHandler exposes explicit retry and cancellation controls for the
// asynchronous extraction/catalog pipeline. Processing state is exposed by
// the document processing-span endpoint, not this control API.
type PaperInfoHandler struct {
	info interfaces.PaperInfoService
}

func NewPaperInfoHandler(info interfaces.PaperInfoService) *PaperInfoHandler {
	return &PaperInfoHandler{info: info}
}

func (h *PaperInfoHandler) RetryExtraction(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	err := h.info.RetryExtraction(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper metadata extraction retry accepted", "document_id", c.Param("documentID"))
	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

func (h *PaperInfoHandler) RetryCatalog(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	err := h.info.RetryCatalogResolution(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper catalog retry accepted", "document_id", c.Param("documentID"))
	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

func (h *PaperInfoHandler) RetryReference(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	err := h.info.RetryReferenceResolution(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper reference resolution retry accepted", "document_id", c.Param("documentID"))
	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

func (h *PaperInfoHandler) Cancel(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	err := h.info.Cancel(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper information cancellation accepted", "document_id", c.Param("documentID"))
	c.JSON(http.StatusAccepted, gin.H{"accepted": true})
}

func (h *PaperInfoHandler) handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
	case errors.Is(err, appservice.ErrPaperInfoExtractionUnavailable),
		errors.Is(err, appservice.ErrPaperInfoCatalogUnavailable),
		strings.Contains(err.Error(), "invalid document id"):
		h.writeError(c, apperrors.NewBadRequestError("paper information retry is unavailable"), err)
	case errors.Is(err, appservice.ErrPaperInfoDeleting):
		h.writeError(c, apperrors.NewConflictError("document is being deleted"), err)
	case errors.Is(err, appservice.ErrPaperInfoAttemptActive):
		h.writeError(c, apperrors.NewConflictError("paper information processing is still active"), err)
	default:
		h.writeError(c, apperrors.NewInternalServerError("operate paper information"), err)
	}
}

func (h *PaperInfoHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "paper information request failed", serverErr, cause)
}
