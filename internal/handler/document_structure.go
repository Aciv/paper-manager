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

type DocumentStructureHandler struct {
	structures interfaces.DocumentStructureService
}

func NewDocumentStructureHandler(structures interfaces.DocumentStructureService) *DocumentStructureHandler {
	return &DocumentStructureHandler{structures: structures}
}

func (h *DocumentStructureHandler) Get(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		abortServerError(c, "document structure request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	structure, err := h.structures.GetDocumentReadingStructure(c.Request.Context(), c.Param("id"))
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			abortServerError(c, "document structure request failed", apperrors.NewNotFoundError("document structure not found"), nil)
		case errors.Is(err, appservice.ErrDocumentStructureTooLarge):
			abortServerError(c, "document structure request failed", apperrors.NewPayloadTooLargeError("document structure exceeds configured limit"), err)
		case errors.Is(err, contextCanceledError(c)):
			return
		default:
			abortServerError(c, "document structure request failed", apperrors.NewInternalServerError("load document structure"), err)
		}
		return
	}
	logger.Debug(c.Request.Context(), "document structure loaded", "document_id", structure.DocumentID,
		"outline_source", structure.Outline.Source, "section_count", len(structure.Outline.Sections))
	c.JSON(http.StatusOK, structure)
}

func contextCanceledError(c *gin.Context) error {
	return c.Request.Context().Err()
}
