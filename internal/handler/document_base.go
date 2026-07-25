package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	baserepo "paperMaster/internal/application/repository"
	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
)

type documentBaseRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// DocumentBaseHandler exposes collection operations without accessing a
// document repository or file storage directly.
type DocumentBaseHandler struct {
	bases interfaces.DocumentBaseService
}

func NewDocumentBaseHandler(bases interfaces.DocumentBaseService) *DocumentBaseHandler {
	return &DocumentBaseHandler{bases: bases}
}

func (h *DocumentBaseHandler) Create(c *gin.Context) {
	var request documentBaseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeBaseError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	if request.Name == nil {
		h.writeBaseError(c, apperrors.NewBadRequestError("document base name is required"), nil)
		return
	}
	description := ""
	if request.Description != nil {
		description = *request.Description
	}
	base, err := h.bases.CreateBase(c.Request.Context(), *request.Name, description)
	if err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document base creation completed", "document_base_id", base.ID)
	c.JSON(http.StatusCreated, gin.H{"documentBase": base})
}

func (h *DocumentBaseHandler) List(c *gin.Context) {
	bases, err := h.bases.ListBases(c.Request.Context())
	if err != nil {
		h.writeBaseError(c, apperrors.NewInternalServerError("list document bases"), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": bases})
}

func (h *DocumentBaseHandler) Get(c *gin.Context) {
	base, err := h.bases.GetBase(c.Request.Context(), c.Param("baseID"))
	if err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"documentBase": base})
}

func (h *DocumentBaseHandler) Update(c *gin.Context) {
	var request documentBaseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.writeBaseError(c, apperrors.NewBadRequestError("request body must be valid JSON"), err)
		return
	}
	if request.Name == nil && request.Description == nil {
		h.writeBaseError(c, apperrors.NewBadRequestError("provide name and/or description"), nil)
		return
	}
	base, err := h.bases.UpdateBase(c.Request.Context(), c.Param("baseID"), request.Name, request.Description)
	if err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document base update completed", "document_base_id", base.ID)
	c.JSON(http.StatusOK, gin.H{"documentBase": base})
}

func (h *DocumentBaseHandler) Delete(c *gin.Context) {
	if err := h.bases.DeleteBase(c.Request.Context(), c.Param("baseID")); err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document base deletion completed", "document_base_id", c.Param("baseID"))
	c.Status(http.StatusNoContent)
}

func (h *DocumentBaseHandler) AddDocument(c *gin.Context) {
	if err := h.bases.AddDocumentToBase(c.Request.Context(), c.Param("baseID"), c.Param("documentID")); err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document base membership added", "document_base_id", c.Param("baseID"), "document_id", c.Param("documentID"))
	c.Status(http.StatusNoContent)
}

func (h *DocumentBaseHandler) RemoveDocument(c *gin.Context) {
	if err := h.bases.RemoveDocumentFromBase(c.Request.Context(), c.Param("baseID"), c.Param("documentID")); err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document base membership removed", "document_base_id", c.Param("baseID"), "document_id", c.Param("documentID"))
	c.Status(http.StatusNoContent)
}

func (h *DocumentBaseHandler) ListDocuments(c *gin.Context) {
	page := 1
	if rawPage := strings.TrimSpace(c.Query("page")); rawPage != "" {
		parsed, err := strconv.Atoi(rawPage)
		if err != nil || parsed < 1 || parsed > types.MaxDocumentSearchPage {
			h.writeBaseError(c, apperrors.NewBadRequestError("page must be between 1 and 10000"), err)
			return
		}
		page = parsed
	}
	result, err := h.bases.ListBaseDocuments(c.Request.Context(), c.Param("baseID"), page)
	if err != nil {
		h.handleBaseServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *DocumentBaseHandler) handleBaseServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		h.writeBaseError(c, apperrors.NewNotFoundError("document base or document not found"), nil)
	case errors.Is(err, baserepo.ErrDuplicateDocumentBaseName):
		h.writeBaseError(c, apperrors.NewConflictError("document base name already exists"), err)
	case errors.Is(err, appservice.ErrInvalidDocumentBaseName), strings.Contains(err.Error(), "invalid document base id"), strings.Contains(err.Error(), "invalid document id"):
		h.writeBaseError(c, apperrors.NewBadRequestError("invalid document base request"), err)
	default:
		h.writeBaseError(c, apperrors.NewInternalServerError("operate document base"), err)
	}
}

func (h *DocumentBaseHandler) writeBaseError(c *gin.Context, serverErr *apperrors.ServerError, cause error) {
	abortServerError(c, "document base request failed", serverErr, cause)
}
