package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type PaperMetadataHandler struct{ documents interfaces.DocumentService }

func NewPaperMetadataHandler(documents interfaces.DocumentService) *PaperMetadataHandler {
	return &PaperMetadataHandler{documents: documents}
}

func (h *PaperMetadataHandler) Get(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("documentID")); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	metadata, err := h.documents.GetPaperMetadata(c.Request.Context(), c.Param("documentID"))
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"paperMetadata": metadata})
}

func (h *PaperMetadataHandler) Update(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("documentID")); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	var update types.PaperMetadataUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		h.writeError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	metadata, err := h.documents.UpdatePaperMetadata(c.Request.Context(), c.Param("documentID"), update)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "paper metadata update completed", "document_id", metadata.DocumentID)
	c.JSON(http.StatusOK, gin.H{"paperMetadata": metadata})
}

func (h *PaperMetadataHandler) Search(c *gin.Context) {
	search := types.PaperSearch{Title: strings.TrimSpace(c.Query("title")), Author: strings.TrimSpace(c.Query("author")), Tags: c.QueryArray("tag"), TagMode: types.PaperTagMode(strings.TrimSpace(c.Query("tagMode"))), Page: 1, PageSize: types.DocumentPageSize}
	if rawPage := c.Query("page"); rawPage != "" {
		page, err := strconv.Atoi(rawPage)
		if err != nil || page < 1 || page > types.MaxDocumentSearchPage {
			h.writeError(c, apperrors.NewBadRequestError("page must be between 1 and 10000"), err)
			return
		}
		search.Page = page
	}
	result, err := h.documents.SearchPaperMetadata(c.Request.Context(), search)
	if err != nil {
		h.handleServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *PaperMetadataHandler) handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		h.writeError(c, apperrors.NewNotFoundError("document not found"), nil)
	case errors.Is(err, appservice.ErrInvalidPaperMetadata), strings.Contains(err.Error(), "invalid document id"), strings.Contains(err.Error(), "invalid tag mode"), strings.Contains(err.Error(), "at least one paper metadata field"):
		h.writeError(c, apperrors.NewBadRequestError("invalid paper metadata request"), err)
	default:
		h.writeError(c, apperrors.NewInternalServerError("operate paper metadata"), err)
	}
}

func (h *PaperMetadataHandler) writeError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "paper metadata request failed", serverErr, cause)
}
