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

// PaperInfoHandler exposes observation and explicit retry controls for the
// asynchronous extraction/catalog pipeline. It never mutates Document.Status.
type PaperInfoHandler struct {
	info interfaces.PaperInfoService
}

func NewPaperInfoHandler(info interfaces.PaperInfoService) *PaperInfoHandler {
	return &PaperInfoHandler{info: info}
}

func (h *PaperInfoHandler) Status(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	state, err := h.info.GetStatus(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"paperInfo": state})
}

func (h *PaperInfoHandler) RetryExtraction(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	state, err := h.info.RetryExtraction(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper metadata extraction retry accepted", "document_id", state.DocumentID)
	c.JSON(http.StatusAccepted, gin.H{"paperInfo": state})
}

func (h *PaperInfoHandler) RetryCatalog(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	state, err := h.info.RetryCatalogResolution(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper catalog retry accepted", "document_id", state.DocumentID)
	c.JSON(http.StatusAccepted, gin.H{"paperInfo": state})
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
	default:
		h.writeError(c, apperrors.NewInternalServerError("operate paper information"), err)
	}
}

func (h *PaperInfoHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "paper information request failed", serverErr, cause)
}
