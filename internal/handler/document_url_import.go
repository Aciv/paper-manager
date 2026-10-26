package handler

import (
	"database/sql"
	"errors"
	"net/http"

	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
)

// EnqueueURLImport accepts downloads independently of the browser lifetime.
// Existing synchronous import endpoints remain available for older clients.
func (h *DocumentHandler) EnqueueURLImport(c *gin.Context) {
	var request struct {
		URLs      []types.DocumentURLImportCommand `json:"urls"`
		BaseIDs   []string                         `json:"baseIds"`
		RequestID string                           `json:"requestId"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid URL import request"), nil)
		return
	}
	if h.urlImports == nil {
		h.urlImportError(c, types.ErrTaskInspectorUnavailable)
		return
	}
	job, err := h.urlImports.EnqueueURLsToBases(c.Request.Context(), request.URLs, request.BaseIDs, request.RequestID)
	if err != nil {
		h.urlImportError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "URL import accepted", "task_id", job.ID, "item_count", job.Total)
	c.JSON(http.StatusAccepted, job)
}

func (h *DocumentHandler) URLImportStatus(c *gin.Context) {
	if h.urlImports == nil {
		h.urlImportError(c, types.ErrTaskInspectorUnavailable)
		return
	}
	job, err := h.urlImports.GetURLImportTask(c.Request.Context(), c.Param("taskID"))
	if err != nil {
		h.urlImportError(c, err)
		return
	}
	c.JSON(http.StatusOK, job)
}

func (h *DocumentHandler) urlImportError(c *gin.Context, err error) {
	if c.Request.Context().Err() != nil {
		return
	}
	switch {
	case errors.Is(err, appservice.ErrInvalidURLImport):
		h.writeError(c, apperrors.NewBadRequestError("invalid URLs, Base IDs or request ID"), nil)
	case errors.Is(err, appservice.ErrURLImportRequestConflict):
		h.writeError(c, apperrors.NewConflictError("request ID already belongs to another import"), nil)
	case errors.Is(err, types.ErrTaskNotFound), errors.Is(err, sql.ErrNoRows):
		h.writeError(c, apperrors.NewNotFoundError("import task or Base not found; completed tasks expire after 24 hours"), nil)
	case errors.Is(err, types.ErrTaskInspectorUnavailable):
		h.writeError(c, apperrors.NewServiceUnavailableError("import queue is unavailable; retry later"), err)
	default:
		h.writeError(c, apperrors.NewInternalServerError("operate URL import task"), err)
	}
}
