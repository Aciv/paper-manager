package handler

import (
	"database/sql"
	"errors"
	"net/http"
	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DocumentProcessHandler owns HTTP operations for asynchronous document
// processing. It is separate from DocumentHandler so retry, reparse, task
// progress, and parser controls can be added without growing file endpoints.
type DocumentProcessHandler struct {
	documents interfaces.DocumentService
}

func NewDocumentProcessHandler(documents interfaces.DocumentService) *DocumentProcessHandler {
	return &DocumentProcessHandler{documents: documents}
}

// CancelDocumentParse cancels queued or active parsing work for a document.
func (h *DocumentProcessHandler) CancelDocumentParse(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	document, err := h.documents.CancelDocumentParse(c.Request.Context(), documentID.String())
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
		case errors.Is(err, appservice.ErrDocumentParseFinished):
			h.writeError(c, apperrors.NewBadRequestError("document parsing has already finished"), err)
		case errors.Is(err, appservice.ErrDocumentDeleting):
			h.writeError(c, apperrors.NewBadRequestError("document is being deleted"), err)
		default:
			h.writeError(c, apperrors.NewInternalServerError("cancel document parsing"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "document cancellation completed", "document_id", document.ID)
	c.JSON(http.StatusOK, gin.H{"document": document})
}

// writeError delegates serialization to the common error middleware while
// retaining process-specific diagnostics in the request log.
func (h *DocumentProcessHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "document process request failed", serverErr, cause)
}
