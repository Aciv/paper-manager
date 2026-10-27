package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/handler"
	"paperMaster/internal/middleware"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type importOptionsDocuments struct {
	interfaces.DocumentService
	options []types.DocumentImportOptions
	calls   int
}

func (s *importOptionsDocuments) CreateDocumentFromFile(
	_ context.Context, _ *multipart.FileHeader, _ string, options ...types.DocumentImportOptions,
) (*types.Document, error) {
	s.calls++
	s.options = options
	return &types.Document{ID: uuid.New()}, nil
}

func (s *importOptionsDocuments) CreateDocumentsFromFiles(
	_ context.Context, files []*multipart.FileHeader, _ []string, options ...[]types.DocumentImportOptions,
) (types.DocumentBatchResult, error) {
	s.calls++
	if len(options) > 0 {
		s.options = options[0]
	}
	return types.DocumentBatchResult{Items: []types.DocumentBatchItemResult{}}, nil
}

func TestHTTPMultipartImportOptions(t *testing.T) {
	for _, test := range []struct {
		name, path string
		count      int
		fields     map[string]string
		status     int
	}{
		{"single", "/upload", 1, map[string]string{
			"metadata":   `{"title":"Title","publishedAt":"2024-01-01"}`,
			"processing": `{"source_lookup":"skip"}`,
		}, 201},
		{"batch", "/batch", 2, map[string]string{
			"metadata":   `[{"title":"First"},{"title":"Second"}]`,
			"processing": `[{"source_lookup":"skip"},{}]`,
		}, 200},
		{"unknown_stage", "/upload", 1, map[string]string{"processing": `{"markdown":"skip"}`}, 400},
		{"misaligned", "/batch", 2, map[string]string{"metadata": `[{"title":"One"}]`}, 400},
		{"bad_mode", "/batch", 2, map[string]string{"processing": `[{"source_lookup":"bad"},{}]`}, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			documents := &importOptionsDocuments{}
			h := handler.NewDocumentHandler(config.Config{}, documents)
			engine := gin.New()
			engine.Use(middleware.ErrorHandler())
			engine.POST("/upload", h.Upload)
			engine.POST("/batch", h.UploadBatch)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for key, value := range test.fields {
				if err := writer.WriteField(key, value); err != nil {
					t.Fatal(err)
				}
			}
			for range test.count {
				field := "file"
				if test.path == "/batch" {
					field = "files"
				}
				file, err := writer.CreateFormFile(field, "paper.pdf")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.Write([]byte("%PDF-1.7\n"))
			}
			_ = writer.Close()
			request := httptest.NewRequest(http.MethodPost, test.path, &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body)
			}
			if test.status == 400 {
				if documents.calls != 0 {
					t.Fatal("invalid options reached persistence")
				}
				return
			}
			if len(documents.options) != test.count ||
				documents.options[0].Processing.SourceLookup != types.SourceLookupSkip {
				t.Fatalf("options were lost: %#v", documents.options)
			}
			encoded, _ := json.Marshal(documents.options)
			if !bytes.Contains(encoded, []byte("title")) {
				t.Fatal("metadata was lost")
			}
		})
	}
}
