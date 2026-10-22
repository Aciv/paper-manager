package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

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

// ReadSectionContent returns one section as raw Markdown. It avoids JSON
// escaping/parsing for large sections while keeping the manifest JSON read
// model unchanged.
func (h *NoteReadingHandler) ReadSectionContent(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	section, err := h.notes.ReadSection(c.Request.Context(), c.Param("id"), c.Param("sectionId"))
	if err != nil {
		h.handleError(c, err)
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{
		"filename": fmt.Sprintf("reading-note-section-%s.md", section.Section.ID),
	}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(section.Markdown))
}

func (h *NoteReadingHandler) ApplyOperations(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, types.MaxDocumentReadingNoteBytes+(1<<20)) // note limit plus JSON overhead
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

func (h *NoteReadingHandler) ApplyEdits(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, types.MaxDocumentReadingNoteBytes+(1<<20)) // note limit plus JSON overhead
	var request types.ReadingNoteEdits
	if err := c.ShouldBindJSON(&request); err != nil {
		abortServerError(c, "reading note edit request failed", apperrors.NewBadRequestError("invalid note edits"), err)
		return
	}
	result, err := h.notes.ApplyEdits(c.Request.Context(), c.Param("id"), request)
	if err != nil {
		h.handleError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document reading note edits applied", "document_id", result.DocumentID,
		"revision", result.Revision, "changed_sections", len(result.ChangedSections), "deleted_sections", len(result.DeletedSectionIDs))
	c.JSON(http.StatusOK, result)
}

func (h *NoteReadingHandler) Put(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	if noteMediaType(c) == "application/json" {
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
		return
	}
	revision, err := strconv.ParseInt(c.Query("expectedRevision"), 10, 64)
	if err != nil || revision < 0 {
		abortServerError(c, "reading note request failed", apperrors.NewBadRequestError("expectedRevision must be non-negative"), err)
		return
	}
	content, err := h.readMarkdownUpload(c)
	if err != nil {
		abortReadingNoteUpload(c, "reading note request failed", err)
		return
	}
	note, err := h.notes.Put(c.Request.Context(), c.Param("id"), revision, content)
	if err != nil {
		h.handleError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document reading note write completed", "document_id", note.DocumentID, "revision", note.Revision)
	c.JSON(http.StatusOK, note)
}

// PutSection replaces one whole section from a raw or multipart Markdown
// upload. It reuses the semantic replaceSection edit, so the target section's
// heading level and the note revision remain enforced.
func (h *NoteReadingHandler) PutSection(c *gin.Context) {
	if !validDocumentParam(c) {
		return
	}
	sectionID, err := uuid.Parse(c.Param("sectionId"))
	if err != nil {
		abortServerError(c, "reading note section upload failed", apperrors.NewBadRequestError("invalid section id"), err)
		return
	}
	revision, err := strconv.ParseInt(c.Query("expectedRevision"), 10, 64)
	if err != nil || revision < 1 {
		abortServerError(c, "reading note section upload failed", apperrors.NewBadRequestError("expectedRevision must be positive"), err)
		return
	}
	content, err := h.readMarkdownUpload(c)
	if err != nil {
		abortReadingNoteUpload(c, "reading note section upload failed", err)
		return
	}
	result, err := h.notes.ReplaceSection(c.Request.Context(), c.Param("id"), sectionID.String(), revision, content)
	if err != nil {
		h.handleError(c, err)
		return
	}
	logger.Info(c.Request.Context(), "document reading note section replaced from upload", "document_id", result.DocumentID,
		"section_id", sectionID, "revision", result.Revision)
	c.JSON(http.StatusOK, result)
}

func noteMediaType(c *gin.Context) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(c.GetHeader("Content-Type")))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("Content-Type"), ";")[0]))
	}
	return strings.ToLower(mediaType)
}

// readMarkdownUpload accepts a bounded raw Markdown body or a multipart file
// field named "file". Multipart bodies bypass the global JSON body limit.
func (h *NoteReadingHandler) readMarkdownUpload(c *gin.Context) (string, error) {
	mediaType := noteMediaType(c)
	maxBytes := int64(types.MaxDocumentReadingNoteBytes)
	// Reject an obviously oversized request before reading any bytes. A
	// multipart envelope adds a small amount of boundary/header overhead.
	contentLimit := maxBytes
	if mediaType == "multipart/form-data" {
		contentLimit += 2 << 20
	}
	if c.Request.ContentLength > contentLimit {
		return "", apperrors.NewPayloadTooLargeError("reading note upload exceeds the configured limit")
	}
	if mediaType == "multipart/form-data" {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+(2<<20))
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			return "", readingNoteUploadReadError(err)
		}
		file, _, err := c.Request.FormFile("file")
		if err != nil {
			return "", apperrors.NewBadRequestError("multipart field file is required")
		}
		defer file.Close()
		return readLimitedMarkdown(file, maxBytes)
	}
	switch mediaType {
	case "text/markdown", "text/plain", "application/octet-stream", "":
		return readLimitedMarkdown(http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+1), maxBytes)
	default:
		return "", apperrors.NewUnsupportedMediaTypeError("reading note upload must be Markdown or multipart/form-data")
	}
}

func readLimitedMarkdown(reader io.Reader, maxBytes int64) (string, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return "", readingNoteUploadReadError(err)
	}
	if int64(len(content)) > maxBytes {
		return "", apperrors.NewPayloadTooLargeError(fmt.Sprintf("reading note exceeds %d bytes", maxBytes))
	}
	if !utf8.Valid(content) {
		return "", apperrors.NewBadRequestError("reading note Markdown must be UTF-8")
	}
	return string(content), nil
}

func readingNoteUploadReadError(err error) error {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return apperrors.NewPayloadTooLargeError("reading note upload exceeds the size limit")
	}
	return apperrors.NewBadRequestError("reading note upload could not be read")
}

func abortReadingNoteUpload(c *gin.Context, event string, err error) {
	var serverErr *apperrors.ServerError
	if errors.As(err, &serverErr) {
		abortServerError(c, event, serverErr, err)
		return
	}
	abortServerError(c, event, apperrors.NewBadRequestError("invalid reading note upload"), err)
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
