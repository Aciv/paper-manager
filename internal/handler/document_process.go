package handler

import (
	"database/sql"
	"errors"
	"net/http"
	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DocumentProcessHandler owns HTTP operations for asynchronous document
// processing. It is separate from DocumentHandler so retry, reparse, task
// progress, and parser controls can be added without growing file endpoints.
type DocumentProcessHandler struct {
	documents interfaces.DocumentService
	spans     interfaces.SpanRecorder
}

func NewDocumentProcessHandler(documents interfaces.DocumentService) *DocumentProcessHandler {
	return &DocumentProcessHandler{documents: documents}
}

// NewDocumentProcessHandlerWithSpanRecorder is the production DI constructor.
func NewDocumentProcessHandlerWithSpanRecorder(documents interfaces.DocumentService, spans interfaces.SpanRecorder) *DocumentProcessHandler {
	return &DocumentProcessHandler{documents: documents, spans: spans}
}

// Status returns user-safe persistent processing state for a document.
func (h *DocumentProcessHandler) Status(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	document, err := h.documents.GetDocumentByID(c.Request.Context(), documentID.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
		} else {
			h.writeError(c, apperrors.NewInternalServerError("load document processing state"), err)
		}
		return
	}
	if h.spans == nil {
		c.JSON(http.StatusOK, gin.H{"processing": types.DocumentProcessingView{DocumentID: document.ID, DocumentStatus: document.Status, Stages: []types.DocumentProcessingSpan{}}})
		return
	}
	view, err := h.spans.GetRunView(c.Request.Context(), *document)
	if err != nil {
		h.writeError(c, apperrors.NewInternalServerError("load document processing state"), err)
		return
	}
	progress, progressErr := h.documents.GetReferenceProgress(c.Request.Context(), document.ID.String())
	if progressErr != nil {
		logger.Warn(c.Request.Context(), "load reference target progress failed", "document_id", document.ID, "error", progressErr)
	} else {
		view.ReferenceProgress = &progress
	}
	c.JSON(http.StatusOK, gin.H{"processing": view})
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

// RetryDocumentParse resumes the persistent document pipeline. Ready documents
// reuse their current Markdown; terminal parse failures start recognition.
func (h *DocumentProcessHandler) RetryDocumentParse(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	document, err := h.documents.RetryDocumentProcess(c.Request.Context(), documentID.String())
	if err != nil {
		if document != nil && errors.Is(err, appservice.ErrDocumentProcessQueue) {
			logger.Warn(c.Request.Context(), "document process retry could not be queued",
				"document_id", document.ID, "status", document.Status)
			c.JSON(http.StatusAccepted, gin.H{
				"document":         document,
				"processingQueued": false,
				"retryable":        true,
			})
			return
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
		case errors.Is(err, appservice.ErrDocumentParseActive):
			h.writeError(c, apperrors.NewConflictError("document parsing is still active"), err)
		case errors.Is(err, appservice.ErrDocumentParseFinished):
			h.writeError(c, apperrors.NewBadRequestError("document parsing is not retryable"), err)
		case errors.Is(err, appservice.ErrDocumentDeleting):
			h.writeError(c, apperrors.NewBadRequestError("document is being deleted"), err)
		default:
			h.writeError(c, apperrors.NewInternalServerError("retry document parsing"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "document process retry accepted", "document_id", document.ID)
	c.JSON(http.StatusAccepted, gin.H{"document": document})
}

// writeError delegates serialization to the common error middleware while
// retaining process-specific diagnostics in the request log.
func (h *DocumentProcessHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "document process request failed", serverErr, cause)
}
