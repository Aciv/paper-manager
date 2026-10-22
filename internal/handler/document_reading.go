package handler

import (
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type DocumentReadingHandler struct {
	documents interfaces.DocumentReadingService
}

func NewDocumentReadingHandler(documents interfaces.DocumentReadingService) *DocumentReadingHandler {
	return &DocumentReadingHandler{documents: documents}
}

// Content streams the original source document.
func (h *DocumentReadingHandler) Content(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	document, reader, filename, err := h.documents.OpenDocumentFile(c.Request.Context(), documentID.String())
	if err != nil {
		h.handleError(c, err, "open document content")
		return
	}
	defer reader.Close()
	modTime := streamModTime(reader, document.UpdatedAt)
	etag := ""
	if hash := strings.TrimSpace(document.FileSHA256); hash != "" {
		etag = `"` + hash + `"`
	}
	serveStream(c, filename, document.ContentType, document.SizeBytes, modTime, etag, reader)
}

// ListBlocks returns source-map blocks for one page, or all pages when the
// page query is omitted.
func (h *DocumentReadingHandler) ListBlocks(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	pageNumber := 0
	if rawPage := strings.TrimSpace(c.Query("page")); rawPage != "" {
		pageNumber, err = strconv.Atoi(rawPage)
		if err != nil || pageNumber < 1 || pageNumber > 100000 {
			abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("page must be between 1 and 100000"), err)
			return
		}
	}
	blocks, err := h.documents.GetDocumentBlocks(c.Request.Context(), documentID.String(), pageNumber)
	if err != nil {
		h.handleError(c, err, "load document blocks")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": blocks})
}

// BlockImage streams one rendered image/table crop after service authorization.
func (h *DocumentReadingHandler) BlockImage(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	blockID, err := uuid.Parse(c.Param("blockID"))
	if err != nil {
		abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("invalid block id"), err)
		return
	}
	reader, filename, err := h.documents.GetDocumentBlockImage(c.Request.Context(), documentID.String(), blockID.String())
	if err != nil {
		h.handleError(c, err, "open document block image")
		return
	}
	defer reader.Close()
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	serveStream(c, filename, contentType, -1, streamModTime(reader, time.Time{}), "", reader)
}

// PageImage renders one full page or normalized page region through the
// parser renderer. It is a synchronous read and never enqueues a task.
func (h *DocumentReadingHandler) PageImage(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abortServerError(c, "document page render failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	pageNumber, err := strconv.Atoi(strings.TrimSpace(c.Param("page")))
	if err != nil || pageNumber < 1 || pageNumber > 100000 {
		abortServerError(c, "document page render failed", apperrors.NewBadRequestError("page must be between 1 and 100000"), err)
		return
	}
	dpi := 0
	if rawDPI := strings.TrimSpace(c.Query("dpi")); rawDPI != "" {
		dpi, err = strconv.Atoi(rawDPI)
		if err != nil || dpi < 1 || dpi > types.MaxPDFRenderDPI {
			abortServerError(c, "document page render failed", apperrors.NewBadRequestError("dpi is outside the supported range"), err)
			return
		}
	}
	var bbox *types.NormalizedPDFRect
	if rawBBox := strings.TrimSpace(c.Query("bbox")); rawBBox != "" {
		parts := strings.Split(rawBBox, ",")
		if len(parts) != 4 {
			abortServerError(c, "document page render failed", apperrors.NewBadRequestError("bbox must be x0,y0,x1,y1"), nil)
			return
		}
		values := make([]float64, 4)
		for index, part := range parts {
			value, parseErr := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if parseErr != nil {
				abortServerError(c, "document page render failed", apperrors.NewBadRequestError("bbox must contain four numbers"), parseErr)
				return
			}
			values[index] = value
		}
		bbox = &types.NormalizedPDFRect{X0: values[0], Y0: values[1], X1: values[2], Y1: values[3]}
	}
	result, err := h.documents.RenderPage(c.Request.Context(), documentID.String(), pageNumber, bbox, dpi)
	if err != nil {
		h.handleError(c, err, "render document page")
		return
	}
	contentType := result.ContentType
	if contentType == "" {
		contentType = "image/png"
	}
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{
		"filename": "page-" + strconv.Itoa(pageNumber) + ".png",
	}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=60")
	c.Data(http.StatusOK, contentType, result.PNG)
}

// Markdown returns the parsed Markdown source as text. The Markdown manifest
// remains the durable metadata; this endpoint is the explicit source read.
func (h *DocumentReadingHandler) Markdown(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	content, err := h.documents.GetDocumentMarkdown(c.Request.Context(), documentID.String())
	if err != nil {
		h.handleError(c, err, "read document Markdown")
		return
	}
	filename := strings.TrimSpace(content.Filename)
	if filename == "" {
		filename = "document.md"
	}
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filename}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, content.ContentType, content.Markdown)
}

// Structure returns the disposable source-outline structure.
func (h *DocumentReadingHandler) Structure(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		abortServerError(c, "document reading request failed", apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	structure, err := h.documents.GetDocumentReadingStructure(c.Request.Context(), documentID.String())
	if err != nil {
		h.handleError(c, err, "load document structure")
		return
	}
	logger.Debug(c.Request.Context(), "document structure loaded", "document_id", structure.DocumentID,
		"outline_source", structure.Outline.Source, "section_count", len(structure.Outline.Sections))
	c.JSON(http.StatusOK, structure)
}

func (h *DocumentReadingHandler) handleError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		abortServerError(c, "document reading request failed", apperrors.NewNotFoundError("document reading data not found"), nil)
	case errors.Is(err, appservice.ErrDocumentStructureTooLarge):
		abortServerError(c, "document reading request failed", apperrors.NewPayloadTooLargeError("document structure exceeds configured limit"), err)
	case errors.Is(err, types.ErrDocumentMarkdownTooLarge):
		abortServerError(c, "document reading request failed", apperrors.NewPayloadTooLargeError("document Markdown exceeds configured read limit"), err)
	case errors.Is(err, types.ErrInvalidPDFRenderRequest):
		abortServerError(c, "document page render failed", apperrors.NewBadRequestError("invalid page render request"), err)
	case errors.Is(err, types.ErrPDFRenderTooLarge):
		abortServerError(c, "document page render failed", apperrors.NewPayloadTooLargeError("rendered page exceeds configured limit"), err)
	case errors.Is(err, types.ErrPDFRenderUnavailable):
		abortServerError(c, "document page render failed", apperrors.NewServiceUnavailableError("PDF renderer is unavailable"), err)
	case errors.Is(err, c.Request.Context().Err()):
		return
	default:
		abortServerError(c, "document reading request failed", apperrors.NewInternalServerError(action), err)
	}
}

// serveStream adds HTTP cache/range semantics. Local storage returns *os.File,
// which is seekable; the fallback keeps other storage drivers working.
func serveStream(
	c *gin.Context, filename, contentType string, size int64,
	modTime time.Time, etag string, reader io.ReadCloser,
) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filename}))
	c.Header("X-Content-Type-Options", "nosniff")
	if etag != "" {
		c.Header("ETag", etag)
	}
	if seeker, ok := reader.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, filename, modTime, seeker)
		return
	}
	if c.Request.Method == http.MethodHead {
		if size >= 0 {
			c.Header("Content-Length", strconv.FormatInt(size, 10))
		}
		c.Status(http.StatusOK)
		return
	}
	c.DataFromReader(http.StatusOK, size, contentType, reader, nil)
}

func streamModTime(reader io.ReadCloser, fallback time.Time) time.Time {
	if statter, ok := reader.(interface{ Stat() (fs.FileInfo, error) }); ok {
		if info, err := statter.Stat(); err == nil {
			return info.ModTime()
		}
	}
	return fallback
}
