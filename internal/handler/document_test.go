package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"paperMaster/internal/application/service"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	"paperMaster/internal/handler"
	"paperMaster/internal/router"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type documentCreatorStub struct {
	document types.Document
	deleted  bool
}

func (s *documentCreatorStub) Create(_ context.Context, doc types.Document) (types.Document, error) {
	s.document = doc
	return doc, nil
}

func (s *documentCreatorStub) Get(_ context.Context, id uuid.UUID) (types.Document, error) {
	if s.deleted || s.document.ID != id {
		return types.Document{}, sql.ErrNoRows
	}
	return s.document, nil
}

func (s *documentCreatorStub) Search(_ context.Context, search types.DocumentSearch) (types.DocumentPage, error) {
	result := types.DocumentPage{Page: search.Page, PageSize: search.PageSize, Items: []types.Document{}}
	if s.deleted || (search.ID != nil && *search.ID != s.document.ID) || (search.DOI != "" && search.DOI != s.document.DOI) {
		return result, nil
	}
	result.Items = append(result.Items, s.document)
	result.Total = 1
	return result, nil
}

func (s *documentCreatorStub) Delete(_ context.Context, id uuid.UUID) error {
	if s.deleted || s.document.ID != id {
		return sql.ErrNoRows
	}
	s.deleted = true
	return nil
}

func (*documentCreatorStub) GetMarkdown(context.Context, uuid.UUID) (types.DocumentMarkdown, error) {
	return types.DocumentMarkdown{}, sql.ErrNoRows
}

func (*documentCreatorStub) ReplaceParsedOutput(context.Context, types.DocumentMarkdown, []types.Block) error {
	return nil
}

func (*documentCreatorStub) ListBlocks(context.Context, uuid.UUID, int) ([]types.Block, error) {
	return []types.Block{}, nil
}

func (*documentCreatorStub) GetBlock(context.Context, uuid.UUID, uuid.UUID) (types.Block, error) {
	return types.Block{}, sql.ErrNoRows
}

func (s *documentCreatorStub) UpdateStatus(_ context.Context, id uuid.UUID, status types.DocumentStatus, message *string) error {
	if s.deleted || s.document.ID != id {
		return sql.ErrNoRows
	}
	s.document.Status, s.document.ErrorMessage = status, message
	return nil
}

func (s *documentCreatorStub) UpdateStatusIfCurrent(_ context.Context, id uuid.UUID, status types.DocumentStatus, message *string, expected []types.DocumentStatus) (bool, error) {
	if s.deleted || s.document.ID != id {
		return false, nil
	}
	for _, current := range expected {
		if s.document.Status == current {
			s.document.Status, s.document.ErrorMessage = status, message
			return true, nil
		}
	}
	return false, nil
}

func (s *documentCreatorStub) UpdateDocumentInfo(_ context.Context, id uuid.UUID, originalFilename, doi *string) (types.Document, error) {
	if s.deleted || s.document.ID != id {
		return types.Document{}, sql.ErrNoRows
	}
	if originalFilename != nil {
		s.document.OriginalFilename = *originalFilename
	}
	if doi != nil {
		s.document.DOI = *doi
	}
	return s.document, nil
}

func newDocumentHandler(storage *fileservice.LocalStorage, repo *documentCreatorStub) *handler.DocumentHandler {
	return handler.NewDocumentHandler(config.Config{MaxUploadSize: fileservice.DefaultMaxUploadSize}, service.NewDocumentService(repo, storage))
}

func TestUploadRouteStoresDocument(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	documents := &documentCreatorStub{}
	documentService := service.NewDocumentService(documents, storage)
	documentHandler := handler.NewDocumentHandler(config.Config{MaxUploadSize: fileservice.DefaultMaxUploadSize}, documentService)
	processHandler := handler.NewDocumentProcessHandler(documentService)
	engine := router.NewRouter(router.RouterParams{DocumentHandler: documentHandler, DocumentProcessHandler: processHandler})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "readme.txt")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write([]byte("uploaded text")); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.WriteField("doi", "10.1000/example-paper"); err != nil {
		t.Fatalf("write DOI field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/document/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if documents.document.OriginalFilename != "readme.txt" || documents.document.DOI != "10.1000/example-paper" || documents.document.Status != types.DocumentStatusUploaded {
		t.Fatalf("unexpected document: %+v", documents.document)
	}

	searchRequest := httptest.NewRequest(http.MethodGet, "/api/document/search?doi=10.1000/example-paper&page=1", nil)
	searchResponse := httptest.NewRecorder()
	engine.ServeHTTP(searchResponse, searchRequest)
	if searchResponse.Code != http.StatusOK || !strings.Contains(searchResponse.Body.String(), "readme.txt") {
		t.Fatalf("search status = %d, body = %s", searchResponse.Code, searchResponse.Body.String())
	}

	contentRequest := httptest.NewRequest(http.MethodGet, "/api/document/"+documents.document.ID.String()+"/content", nil)
	contentResponse := httptest.NewRecorder()
	engine.ServeHTTP(contentResponse, contentRequest)
	if contentResponse.Code != http.StatusOK || contentResponse.Body.String() != "uploaded text" {
		t.Fatalf("content status = %d, body = %q", contentResponse.Code, contentResponse.Body.String())
	}

	blocksRequest := httptest.NewRequest(http.MethodGet, "/api/document/"+documents.document.ID.String()+"/blocks?page=1", nil)
	blocksResponse := httptest.NewRecorder()
	engine.ServeHTTP(blocksResponse, blocksRequest)
	if blocksResponse.Code != http.StatusOK || !strings.Contains(blocksResponse.Body.String(), `"items":[]`) {
		t.Fatalf("blocks status = %d, body = %s", blocksResponse.Code, blocksResponse.Body.String())
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "/api/document/"+documents.document.ID.String()+"/cancel", nil)
	cancelResponse := httptest.NewRecorder()
	engine.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelResponse.Code, cancelResponse.Body.String())
	}
	if documents.document.Status != types.DocumentStatusCancelled {
		t.Fatalf("document status = %q, want cancelled", documents.document.Status)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/document/"+documents.document.ID.String(), nil)
	deleteResponse := httptest.NewRecorder()
	engine.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if !documents.deleted {
		t.Fatal("document metadata was not deleted")
	}
	if _, err := storage.Open(context.Background(), documents.document.StorageKey); err == nil {
		t.Fatal("stored file still exists after deletion")
	}
}

func TestDocumentHandlerUsesCustomErrorResponse(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	engine := router.NewRouter(router.RouterParams{DocumentHandler: newDocumentHandler(storage, &documentCreatorStub{})})

	request := httptest.NewRequest(http.MethodPost, "/api/document/upload", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":1000`) {
		t.Fatalf("expected custom error response, body = %s", response.Body.String())
	}
}
