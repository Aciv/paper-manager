package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type NoteReadingHandler struct {
	notes interfaces.NoteReadingService
}

func NewNoteReadingHandler(notes interfaces.NoteReadingService) *NoteReadingHandler {
	return &NoteReadingHandler{notes: notes}
}

func (h *NoteReadingHandler) Get(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	note, err := h.notes.GetManifest(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, note)
}

func (h *NoteReadingHandler) ReadSection(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	section, err := h.notes.ReadSection(c.Request.Context(), c.Param("id"), c.Param("sectionId"))
	if err != nil {
		h.handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, section)
}

func (h *NoteReadingHandler) ApplyOperations(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, types.MaxDocumentReadingNoteBytes+(1<<20)) // 10 MiB note plus JSON overhead
	var request types.ReadingNoteOperations
	if err := c.ShouldBindJSON(&request); err != nil {
		abortServerError(c, "reading note operation request failed", apperrors.NewBadRequestError("invalid note operations"), err)
		return
	}
	result, err := h.notes.ApplyOperations(c.Request.Context(), c.Param("id"), request)
	if err != nil {
		h.handleError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document reading note operations applied", "document_id", result.DocumentID,
		"revision", result.Revision, "changed_sections", len(result.ChangedSections), "deleted_sections", len(result.DeletedSectionIDs))
	c.JSON(http.StatusOK, result)
}

func (h *NoteReadingHandler) Put(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	mediaType := strings.TrimSpace(strings.Split(strings.ToLower(c.GetHeader("Content-Type")), ";")[0])
	if mediaType == "text/markdown" {
		revision, err := strconv.ParseInt(c.Query("expectedRevision"), 10, 64)
		if err != nil || revision < 0 {
			abortServerError(c, "reading note request failed", apperrors.NewBadRequestError("expectedRevision must be non-negative"), err)
			return
		}
		content, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, types.MaxDocumentReadingNoteBytes+1))
		if err != nil || len(content) > types.MaxDocumentReadingNoteBytes {
			abortServerError(c, "reading note request failed", apperrors.NewPayloadTooLargeError("reading note exceeds 10 MiB"), err)
			return
		}
		note, err := h.notes.Put(c.Request.Context(), c.Param("id"), revision, string(content))
		if err != nil {
			h.handleError(c, err)
			return
		}
		logger.Info(c.Request.Context(), "document reading note write completed", "document_id", note.DocumentID, "revision", note.Revision)
		c.JSON(http.StatusOK, note)
		return
	}
	var request struct {
		Markdown         *string `json:"markdown" binding:"required"`
		ExpectedRevision int64   `json:"expectedRevision"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		abortServerError(c, "reading note request failed", apperrors.NewBadRequestError("request body must contain Markdown and expectedRevision"), err)
		return
	}
	note, err := h.notes.Put(c.Request.Context(), c.Param("id"), request.ExpectedRevision, *request.Markdown)
	if err != nil {
		h.handleError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document reading note write completed", "document_id", note.DocumentID, "revision", note.Revision)
	c.JSON(http.StatusOK, note)
}

func (h *NoteReadingHandler) Delete(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	revision, err := strconv.ParseInt(c.Query("expectedRevision"), 10, 64)
	if err != nil || revision < 1 {
		abortServerError(c, "reading note request failed", apperrors.NewBadRequestError("expectedRevision must be positive"), err)
		return
	}
	if err := h.notes.Delete(c.Request.Context(), c.Param("id"), revision); err != nil {
		h.handleError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document reading note delete completed", "document_id", c.Param("id"), "revision", revision)
	c.Status(http.StatusNoContent)
}

func (h *NoteReadingHandler) Export(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	note, err := h.notes.Export(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.handleError(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="reading-note-%s-r%d.md"`, note.DocumentID, note.Revision))
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(note.Markdown))
}

func validDocumentParam(c *gin.Context) bool {
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		abortServerError(c, "document request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return false
	}
	return true
}

func (h *NoteReadingHandler) handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		abortServerError(c, "reading note request failed", apperrors.NewNotFoundError("document or reading note not found"), nil)
	case errors.Is(err, types.ErrReadingNoteConflict):
		abortServerError(c, "reading note request failed", apperrors.NewConflictError("reading note revision changed"), nil)
	case errors.Is(err, types.ErrReadingNoteUnavailable):
		abortServerError(c, "reading note request failed", apperrors.NewServiceUnavailableError("reading note is temporarily unavailable during full import"), nil)
	case errors.Is(err, types.ErrInvalidReadingNote):
		abortServerError(c, "reading note request failed", apperrors.NewBadRequestError(err.Error()), err)
	case errors.Is(err, c.Request.Context().Err()):
		return
	default:
		abortServerError(c, "reading note request failed", apperrors.NewInternalServerError("operate document reading note"), err)
	}
}
