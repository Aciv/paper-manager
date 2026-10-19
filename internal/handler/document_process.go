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
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// DocumentProcessHandler owns HTTP operations for asynchronous document
// processing. It is separate from DocumentHandler so retry, reparse, task
// progress, and parser controls can be added without growing file endpoints.
type DocumentProcessHandler struct {
	documents  interfaces.DocumentService
	processing interfaces.ProcessingViewQuery
}

func NewDocumentProcessHandler(documents interfaces.DocumentService) *DocumentProcessHandler {
	return &DocumentProcessHandler{documents: documents}
}

// NewDocumentProcessHandlerWithProcessingViewQuery is the production DI constructor.
func NewDocumentProcessHandlerWithProcessingViewQuery(documents interfaces.DocumentService, processing interfaces.ProcessingViewQuery) *DocumentProcessHandler {
	return &DocumentProcessHandler{documents: documents, processing: processing}
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
	if h.processing == nil {
		states := make([]types.DocumentStageState, 0, 4)
		for _, stage := range []types.DocumentStage{types.DocumentStageMarkdown, types.DocumentStageMetadataExtraction, types.DocumentStageSourceLookup, types.DocumentStageReferenceLookup} {
			if state, stateErr := document.StageState(stage); stateErr == nil {
				states = append(states, state)
			}
		}
		c.JSON(http.StatusOK, gin.H{"processing": types.DocumentProcessingView{DocumentID: document.ID, SourceState: document.SourceState, CanonicalDocumentID: document.CanonicalDocumentID, StageStates: states, Stages: []types.DocumentProcessingSpan{}}})
		return
	}
	view, err := h.processing.GetView(c.Request.Context(), *document)
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

// Projections returns a bounded batch read model for library and citation
// progress first paint. It replaces per-document requests without exposing
// repository details to the client.
func (h *DocumentProcessHandler) Projections(c *gin.Context) {
	var query types.DocumentProjectionQuery
	if err := c.ShouldBindJSON(&query); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	if len(query.DocumentIDs) == 0 {
		h.writeError(c, apperrors.NewBadRequestError("at least one document id is required"), nil)
		return
	}
	items, err := h.documents.GetDocumentProjections(c.Request.Context(), query)
	if err != nil {
		if strings.Contains(err.Error(), "invalid document id") ||
			strings.Contains(err.Error(), "at most") {
			h.writeError(c, apperrors.NewBadRequestError("invalid document projection request"), err)
			return
		}
		h.writeError(c, apperrors.NewInternalServerError("load document projections"), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
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
				"document_id", document.ID, "markdown_status", document.MarkdownStatus)
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
