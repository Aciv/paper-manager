package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
)

type BibliographyHandler struct {
	service interfaces.BibliographyService
}

func NewBibliographyHandler(service interfaces.BibliographyService) *BibliographyHandler {
	return &BibliographyHandler{service: service}
}

func (h *BibliographyHandler) Generate(c *gin.Context) {
	requestID, _ := c.Request.Context().Value(types.RequestIDContextKey).(string)
	result, err := h.service.EnqueueBaseBibliography(c.Request.Context(), c.Param("baseID"), requestID)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			abortServerError(c, "generate base bibliography failed", apperrors.NewNotFoundError("document base not found"), nil)
		case errors.Is(err, types.ErrBibliographyBaseTooLarge):
			abortServerError(c, "generate base bibliography failed", apperrors.NewPayloadTooLargeError("document base exceeds bibliography export limit"), nil)
		case errors.Is(err, types.ErrBibliographyOutputTooLarge):
			abortServerError(c, "generate base bibliography failed", apperrors.NewPayloadTooLargeError("generated bibliography exceeds output limit"), nil)
		case errors.Is(err, types.ErrBibliographyTaskUnavailable):
			abortServerError(c, "queue base bibliography failed", apperrors.NewServiceUnavailableError("bibliography generation queue is unavailable"), err)
		case strings.Contains(err.Error(), "invalid document base id"):
			abortServerError(c, "generate base bibliography failed", apperrors.NewBadRequestError("invalid document base id"), err)
		default:
			abortServerError(c, "generate base bibliography failed", apperrors.NewInternalServerError("generate base bibliography"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "base bibliography generation accepted",
		"document_base_id", result.BaseID, "task_id", result.ID)
	c.JSON(http.StatusAccepted, gin.H{"task": result})
}

func (h *BibliographyHandler) Status(c *gin.Context) {
	result, err := h.service.GetBaseBibliographyTask(
		c.Request.Context(), c.Param("baseID"), c.Param("taskID"),
	)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, types.ErrBibliographyTaskNotFound), errors.Is(err, sql.ErrNoRows):
			abortServerError(c, "get bibliography task failed", apperrors.NewNotFoundError("bibliography generation task not found"), nil)
		case errors.Is(err, types.ErrBibliographyTaskUnavailable):
			abortServerError(c, "get bibliography task failed", apperrors.NewServiceUnavailableError("bibliography generation status is unavailable"), err)
		case strings.Contains(err.Error(), "invalid document base id"):
			abortServerError(c, "get bibliography task failed", apperrors.NewBadRequestError("invalid document base id"), err)
		default:
			abortServerError(c, "get bibliography task failed", apperrors.NewInternalServerError("get bibliography generation status"), err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": result})
}
