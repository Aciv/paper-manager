package handler_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"paperMaster/internal/application/service"
	"paperMaster/internal/handler"
	"paperMaster/internal/router"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func selectionTestEngine(repo *documentCreatorStub) *routerFixture {
	processor := service.NewDocumentSelectionProcessor(repo)
	selectionHandler := handler.NewDocumentSelectionHandler(processor)
	return &routerFixture{engine: router.NewRouter(router.RouterParams{DocumentSelectionHandler: selectionHandler})}
}

type routerFixture struct{ engine http.Handler }

func (f *routerFixture) post(t *testing.T, documentID uuid.UUID, fields map[string]string, imageData []byte, imageType string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if _, exists := fields["document_id"]; !exists {
		if err := writer.WriteField("document_id", documentID.String()); err != nil {
			t.Fatalf("write document_id: %v", err)
		}
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
	}
	if imageData != nil {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="image"; filename="selection.png"`)
		header.Set("Content-Type", imageType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create image part: %v", err)
		}
		if _, err := part.Write(imageData); err != nil {
			t.Fatalf("write image: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/document/"+documentID.String()+"/selection/process", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	f.engine.ServeHTTP(response, request)
	return response
}

func selectionPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var data bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&data, canvas); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return data.Bytes()
}

func areaFields() map[string]string {
	return map[string]string{"kind": "area", "page_number": "2", "x0": "0.1", "y0": "0.2", "x1": "0.6", "y1": "0.8"}
}

func TestDocumentSelectionAcceptsValidAreaCapture(t *testing.T) {
	documentID := uuid.New()
	fixture := selectionTestEngine(&documentCreatorStub{document: types.Document{ID: documentID}})
	response := fixture.post(t, documentID, areaFields(), selectionPNG(t, 24, 16), "image/png")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"imageWidth":24`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDocumentSelectionRejectsBlockFromAnotherDocument(t *testing.T) {
	documentID := uuid.New()
	otherDocumentID := uuid.New()
	blockID := uuid.New()
	fixture := selectionTestEngine(&documentCreatorStub{document: types.Document{ID: documentID}, blocks: []types.Block{{ID: blockID, DocumentID: otherDocumentID}}})
	response := fixture.post(t, documentID, map[string]string{"kind": "block", "block_id": blockID.String()}, nil, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDocumentSelectionRejectsInvalidCoordinates(t *testing.T) {
	documentID := uuid.New()
	fields := areaFields()
	fields["x1"] = "1.5"
	response := selectionTestEngine(&documentCreatorStub{document: types.Document{ID: documentID}}).post(t, documentID, fields, selectionPNG(t, 10, 10), "image/png")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDocumentSelectionRejectsUnsupportedMIME(t *testing.T) {
	documentID := uuid.New()
	response := selectionTestEngine(&documentCreatorStub{document: types.Document{ID: documentID}}).post(t, documentID, areaFields(), selectionPNG(t, 10, 10), "image/jpeg")
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDocumentSelectionRejectsOversizedUpload(t *testing.T) {
	documentID := uuid.New()
	oversized := bytes.Repeat([]byte{'x'}, types.MaxDocumentSelectionImageSize+1)
	response := selectionTestEngine(&documentCreatorStub{document: types.Document{ID: documentID}}).post(t, documentID, areaFields(), oversized, "image/png")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDocumentSelectionRejectsExcessiveImageDimensions(t *testing.T) {
	documentID := uuid.New()
	width := types.MaxDocumentSelectionDimension + 1
	response := selectionTestEngine(&documentCreatorStub{document: types.Document{ID: documentID}}).post(t, documentID, areaFields(), selectionPNG(t, width, 1), "image/png")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("width=%s status=%d body=%s", strconv.Itoa(width), response.Code, response.Body.String())
	}
}

func TestDocumentSelectionProcessorHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	processor := service.NewDocumentSelectionProcessor(&documentCreatorStub{})
	_, err := processor.ProcessSelection(ctx, types.DocumentSelectionInput{DocumentID: uuid.New(), Kind: types.DocumentSelectionText, Text: "selected"})
	if err != context.Canceled {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}
