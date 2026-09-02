package handler

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image/png"
	"io"
	"mime/multipart"
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

const documentSelectionMultipartOverhead = 1 << 20

// DocumentSelectionHandler owns the HTTP validation boundary for transient
// text, block, and browser-created area captures.
type DocumentSelectionHandler struct {
	processor interfaces.DocumentSelectionProcessor
}

func NewDocumentSelectionHandler(processor interfaces.DocumentSelectionProcessor) *DocumentSelectionHandler {
	return &DocumentSelectionHandler{processor: processor}
}

// Process accepts one multipart selection. It keeps screenshot bytes in the
// request only and releases multipart resources before returning.
func (h *DocumentSelectionHandler) Process(c *gin.Context) {
	documentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		h.writeError(c, apperrors.NewBadRequestError("invalid document id"), err)
		return
	}
	if !strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		h.writeError(c, apperrors.NewUnsupportedMediaTypeError("selection request must use multipart/form-data"), nil)
		return
	}
	c.Request.Body = http.MaxBytesReader(
		c.Writer, c.Request.Body,
		types.MaxDocumentSelectionImageSize+documentSelectionMultipartOverhead,
	)
	if err := c.Request.ParseMultipartForm(types.MaxDocumentSelectionImageSize); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.writeError(c, apperrors.NewPayloadTooLargeError("selection capture exceeds 4 MiB"), err)
			return
		}
		h.writeError(c, apperrors.NewBadRequestError("invalid multipart selection request"), err)
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}

	input := types.DocumentSelectionInput{
		DocumentID: documentID,
		Kind:       strings.TrimSpace(c.PostForm("kind")),
	}
	formDocumentID, err := uuid.Parse(strings.TrimSpace(c.PostForm("document_id")))
	if err != nil || formDocumentID != documentID {
		h.writeError(c, apperrors.NewBadRequestError("document_id must match the route document"), err)
		return
	}
	switch input.Kind {
	case types.DocumentSelectionText:
		input.Text = strings.TrimSpace(c.PostForm("text"))
		if input.Text == "" || !utf8.ValidString(input.Text) ||
			utf8.RuneCountInString(input.Text) > types.MaxDocumentSelectionTextRunes {
			h.writeError(c, apperrors.NewBadRequestError("selected text must contain 1 to 16384 valid UTF-8 characters"), nil)
			return
		}
		if input.PageNumber, err = optionalSelectionPage(c.PostForm("page_number")); err != nil {
			h.writeError(c, apperrors.NewBadRequestError("page_number must be between 1 and 100000"), err)
			return
		}
		if input.Region, err = optionalSelectionRegion(c); err != nil {
			h.writeError(c, apperrors.NewBadRequestError("text selection region must be normalized"), err)
			return
		}
	case types.DocumentSelectionBlock:
		input.BlockID, err = uuid.Parse(strings.TrimSpace(c.PostForm("block_id")))
		if err != nil {
			h.writeError(c, apperrors.NewBadRequestError("block_id must be a UUID"), err)
			return
		}
	case types.DocumentSelectionArea:
		if err := h.bindArea(c, &input); err != nil {
			var serverErr *apperrors.ServerError
			if errors.As(err, &serverErr) {
				h.writeError(c, serverErr, err)
			} else {
				h.writeError(c, apperrors.NewBadRequestError("invalid area selection"), err)
			}
			return
		}
	default:
		h.writeError(c, apperrors.NewBadRequestError("kind must be text, block, or area"), nil)
		return
	}

	result, err := h.processor.ProcessSelection(c.Request.Context(), input)
	// The client is already gone; avoid manufacturing a second HTTP error and
	// let every request-owned byte become unreachable immediately.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			h.writeError(c, apperrors.NewNotFoundError("document or block not found"), nil)
		case errors.Is(err, types.ErrInvalidDocumentSelection):
			h.writeError(c, apperrors.NewBadRequestError("invalid document selection"), err)
		default:
			h.writeError(c, apperrors.NewInternalServerError("process document selection"), err)
		}
		return
	}
	logger.Info(c.Request.Context(), "transient document selection accepted",
		"document_id", documentID, "selection_kind", input.Kind)
	c.JSON(http.StatusOK, gin.H{"result": result})
}

func (h *DocumentSelectionHandler) bindArea(c *gin.Context, input *types.DocumentSelectionInput) error {
	pageNumber, err := strconv.Atoi(strings.TrimSpace(c.PostForm("page_number")))
	if err != nil || pageNumber < 1 || pageNumber > types.MaxDocumentSelectionPage {
		return errors.New("page_number is out of range")
	}
	region := types.NormalizedDocumentRegion{}
	coordinates := []struct {
		name string
		to   *float64
	}{{"x0", &region.X0}, {"y0", &region.Y0}, {"x1", &region.X1}, {"y1", &region.Y1}}
	for _, coordinate := range coordinates {
		value, err := strconv.ParseFloat(strings.TrimSpace(c.PostForm(coordinate.name)), 64)
		if err != nil {
			return errors.New(coordinate.name + " must be a number")
		}
		*coordinate.to = value
	}
	if !region.Valid() {
		return errors.New("normalized region must be non-empty and within the page")
	}
	file, header, err := c.Request.FormFile("image")
	if err != nil {
		return errors.New("multipart field 'image' is required")
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > types.MaxDocumentSelectionImageSize {
		return apperrors.NewPayloadTooLargeError("selection capture exceeds 4 MiB")
	}
	if strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type"))) != "image/png" {
		return apperrors.NewUnsupportedMediaTypeError("selection capture must be image/png")
	}
	data, err := readBoundedSelectionImage(file)
	if err != nil {
		return err
	}
	if http.DetectContentType(data) != "image/png" {
		return apperrors.NewUnsupportedMediaTypeError("selection capture must contain PNG data")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return errors.New("selection capture is not a valid PNG")
	}
	if config.Width < 1 || config.Height < 1 ||
		config.Width > types.MaxDocumentSelectionDimension ||
		config.Height > types.MaxDocumentSelectionDimension ||
		config.Width*config.Height > types.MaxDocumentSelectionPixels {
		return apperrors.NewPayloadTooLargeError("selection capture dimensions exceed the safe limit")
	}
	input.PageNumber = pageNumber
	input.Region = &region
	input.ImageMIME = "image/png"
	input.ImageBytes = data
	input.ImageWidth = config.Width
	input.ImageHeight = config.Height
	return nil
}

func readBoundedSelectionImage(file multipart.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, types.MaxDocumentSelectionImageSize+1))
	if err != nil {
		return nil, errors.New("read selection capture")
	}
	if len(data) > types.MaxDocumentSelectionImageSize {
		return nil, apperrors.NewPayloadTooLargeError("selection capture exceeds 4 MiB")
	}
	return data, nil
}

func optionalSelectionPage(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	pageNumber, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || pageNumber < 1 || pageNumber > types.MaxDocumentSelectionPage {
		return 0, errors.New("page number is out of range")
	}
	return pageNumber, nil
}

func optionalSelectionRegion(c *gin.Context) (*types.NormalizedDocumentRegion, error) {
	raw := []string{c.PostForm("x0"), c.PostForm("y0"), c.PostForm("x1"), c.PostForm("y1")}
	if strings.TrimSpace(strings.Join(raw, "")) == "" {
		return nil, nil
	}
	region := &types.NormalizedDocumentRegion{}
	values := []*float64{&region.X0, &region.Y0, &region.X1, &region.Y1}
	for index, item := range raw {
		value, err := strconv.ParseFloat(strings.TrimSpace(item), 64)
		if err != nil {
			return nil, errors.New("region coordinate " + strconv.Itoa(index) + " is invalid")
		}
		*values[index] = value
	}
	if !region.Valid() {
		return nil, errors.New("region is empty or outside the page")
	}
	return region, nil
}

func (h *DocumentSelectionHandler) writeError(
	c *gin.Context, serverErr *apperrors.ServerError, cause error,
) {
	abortServerError(c, "document selection request failed", serverErr, cause)
}
