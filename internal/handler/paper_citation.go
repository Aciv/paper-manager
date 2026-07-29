package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// PaperCitationHandler owns HTTP operations for the directional paper graph.
// It is kept separate from PaperMetadataHandler so metadata patches do not
// grow into relationship-management endpoints.
type PaperCitationHandler struct{ papers interfaces.PaperService }

func NewPaperCitationHandler(papers interfaces.PaperService) *PaperCitationHandler {
	return &PaperCitationHandler{papers: papers}
}

func (h *PaperCitationHandler) List(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	items, err := h.papers.ListCitations(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *PaperCitationHandler) ListCitedBy(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	items, err := h.papers.ListCitedBy(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *PaperCitationHandler) Add(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	var input types.PaperCitationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	citation, err := h.papers.AddCitation(c.Request.Context(), c.Param("documentID"), input)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper citation created", "citing_document_id", citation.CitingDocumentID, "cited_document_id", citation.CitedDocumentID)
	c.JSON(http.StatusCreated, gin.H{"citation": citation})
}

func (h *PaperCitationHandler) Replace(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	var inputs []types.PaperCitationInput
	if err := c.ShouldBindJSON(&inputs); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be a JSON citation array"), err)
		return
	}
	items, err := h.papers.ReplaceCitations(c.Request.Context(), c.Param("documentID"), inputs)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper citations replaced", "document_id", c.Param("documentID"), "citation_count", len(items))
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *PaperCitationHandler) Remove(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	if _, err := uuid.Parse(c.Param("citedDocumentID")); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid cited document id"), err)
		return
	}
	if err := h.papers.RemoveCitation(c.Request.Context(), c.Param("documentID"), c.Param("citedDocumentID")); err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper citation removed", "citing_document_id", c.Param("documentID"), "cited_document_id", c.Param("citedDocumentID"))
	c.Status(http.StatusNoContent)
}

func (h *PaperCitationHandler) Resolve(c *gin.Context) {
	if !validPaperDocumentParam(c, h.writeError) {
		return
	}
	var request struct {
		ResolvedDocumentID string `json:"resolvedDocumentId"`
		RawReference       string `json:"rawReference"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	if _, err := uuid.Parse(strings.TrimSpace(request.ResolvedDocumentID)); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid resolved document id"), err)
		return
	}
	citation, err := h.papers.ResolveCitation(c.Request.Context(), c.Param("documentID"), request.ResolvedDocumentID, request.RawReference)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper citation resolved", "citing_document_id", citation.CitingDocumentID, "cited_document_id", citation.CitedDocumentID)
	c.JSON(http.StatusOK, gin.H{"citation": citation})
}

func (h *PaperCitationHandler) handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
	case errors.Is(err, types.ErrInvalidPaperCitation),
		errors.Is(err, types.ErrSelfPaperCitation),
		errors.Is(err, appservice.ErrUnknownPaperCannotCite),
		errors.Is(err, appservice.ErrCitationTargetNotResolved),
		strings.Contains(err.Error(), "invalid document id"):
		h.writeError(c, apperrors.NewBadRequestError("invalid paper citation request"), err)
	default:
		h.writeError(c, apperrors.NewInternalServerError("operate paper citation"), err)
	}
}

func (h *PaperCitationHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "paper citation request failed", serverErr, cause)
}

func validPaperDocumentParam(c *gin.Context, writeError func(*gin.Context, *apperrors.ServerError, error)) bool {
	id, err := uuid.Parse(c.Param("documentID"))
	if err != nil || id == uuid.Nil {
		if err == nil {
			err = errors.New("document id must be non-zero")
		}
		writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return false
	}
	return true
}
