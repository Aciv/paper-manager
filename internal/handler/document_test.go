package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"github.com/hibiken/asynq"
)

type failingDocumentTaskEnqueuer struct{}

func (*failingDocumentTaskEnqueuer) EnqueueContext(
	context.Context,
	*asynq.Task,
	...asynq.Option,
) (*asynq.TaskInfo, error) {
	return nil, errors.New("queue unavailable")
}

type documentCreatorStub struct {
	document types.Document
	deleted  bool
	blocks   []types.Block
}

func (*documentCreatorStub) CitationTargetProgress(context.Context, uuid.UUID) (types.PaperReferenceProgress, error) {
	return types.PaperReferenceProgress{}, nil
}

func (s *documentCreatorStub) GetStage(_ context.Context, _ uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, error) {
	return s.document.StageState(stage)
}

func (s *documentCreatorStub) QueueStageNewGeneration(_ context.Context, _ uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, bool, error) {
	if stage != types.DocumentStageMarkdown || s.document.MarkdownStatus.Active() {
		return types.DocumentStageState{Stage: stage, Status: s.document.MarkdownStatus, Generation: s.document.MarkdownGeneration}, false, nil
	}
	s.document.MarkdownStatus = types.DocumentStageQueued
	s.document.MarkdownGeneration++
	return types.DocumentStageState{Stage: stage, Status: s.document.MarkdownStatus, Generation: s.document.MarkdownGeneration}, true, nil
}

func (*documentCreatorStub) TryStartStage(context.Context, uuid.UUID, types.DocumentStage, int64) (types.DocumentStageState, bool, error) {
	return types.DocumentStageState{}, false, nil
}

func (*documentCreatorStub) MarkStageRetryQueued(context.Context, uuid.UUID, types.DocumentStage, int64) (types.DocumentStageState, bool, error) {
	return types.DocumentStageState{}, false, nil
}

func (s *documentCreatorStub) FinishStage(_ context.Context, _ uuid.UUID, stage types.DocumentStage, generation int64, status types.DocumentStageStatus, _, _ string) (types.DocumentStageState, bool, error) {
	if stage != types.DocumentStageMarkdown || s.document.MarkdownGeneration != generation || !s.document.MarkdownStatus.Active() {
		return types.DocumentStageState{Stage: stage, Status: s.document.MarkdownStatus, Generation: s.document.MarkdownGeneration}, false, nil
	}
	s.document.MarkdownStatus = status
	return types.DocumentStageState{Stage: stage, Status: status, Generation: generation}, true, nil
}

func (*documentCreatorStub) CancelStage(context.Context, uuid.UUID, types.DocumentStage, int64) (types.DocumentStageState, bool, error) {
	return types.DocumentStageState{}, false, nil
}

func (*documentCreatorStub) AllowsStageWork(context.Context, uuid.UUID, types.DocumentStage, int64) (bool, error) {
	return false, nil
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

func (s *documentCreatorStub) EnsureUnknown(
	_ context.Context, id uuid.UUID,
) (types.Document, bool, error) {
	if !s.deleted && s.document.ID == id {
		return s.document, false, nil
	}
	s.deleted = false
	s.document = types.Document{
		ID:          id,
		StorageKey:  types.UnknownDocumentStorageScheme + id.String(),
		SourceState: types.DocumentSourceMissing,
	}
	return s.document, true, nil
}

func (s *documentCreatorStub) DeleteUnknownIfUnreferenced(
	_ context.Context, id uuid.UUID,
) (bool, error) {
	if s.deleted || s.document.ID != id || s.document.SourceState != types.DocumentSourceMissing {
		return false, nil
	}
	s.deleted = true
	return true, nil
}

func (s *documentCreatorStub) Search(_ context.Context, search types.DocumentSearch) (types.DocumentPage, error) {
	result := types.DocumentPage{Page: search.Page, PageSize: search.PageSize, Items: []types.Document{}}
	if s.deleted ||
		(search.ID != nil && *search.ID != s.document.ID) ||
		(search.DOI != "" && search.DOI != s.document.DOI) ||
		(search.FileSHA256 != "" && search.FileSHA256 != s.document.FileSHA256) {
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

func (s *documentCreatorStub) MarkDeleting(_ context.Context, id uuid.UUID) (types.DocumentSourceState, bool, error) {
	if s.deleted || s.document.ID != id {
		return "", false, sql.ErrNoRows
	}
	previous := s.document.SourceState
	if previous == types.DocumentSourceDeleting || previous == types.DocumentSourceMerged {
		return previous, false, nil
	}
	s.document.SourceState = types.DocumentSourceDeleting
	return previous, true, nil
}

func (s *documentCreatorStub) RestoreSourceState(_ context.Context, id uuid.UUID, state types.DocumentSourceState) error {
	if !s.deleted && s.document.ID == id && s.document.SourceState == types.DocumentSourceDeleting {
		s.document.SourceState = state
	}
	return nil
}

func (*documentCreatorStub) GetMarkdown(context.Context, uuid.UUID) (types.DocumentMarkdown, error) {
	return types.DocumentMarkdown{}, sql.ErrNoRows
}

func (*documentCreatorStub) ReplaceParsedOutput(context.Context, types.DocumentMarkdown, []types.Block, int64) error {
	return nil
}

func (*documentCreatorStub) ListBlocks(context.Context, uuid.UUID, int) ([]types.Block, error) {
	return []types.Block{}, nil
}

func (s *documentCreatorStub) GetBlock(_ context.Context, documentID, blockID uuid.UUID) (types.Block, error) {
	for _, block := range s.blocks {
		if block.DocumentID == documentID && block.ID == blockID {
			return block, nil
		}
	}
	return types.Block{}, sql.ErrNoRows
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

func (s *documentCreatorStub) SetDOIIfEmpty(_ context.Context, id uuid.UUID, doi string) (bool, error) {
	if s.deleted || s.document.ID != id || s.document.DOI != "" {
		return false, nil
	}
	s.document.DOI = doi
	return true, nil
}

func (s *documentCreatorStub) PromoteUnknown(_ context.Context, document types.Document) (bool, error) {
	if s.document.ID != document.ID || s.document.SourceState != types.DocumentSourceMissing {
		return false, nil
	}
	document.SourceState = types.DocumentSourceAvailable
	s.document = document
	return true, nil
}

func (s *documentCreatorStub) MergeIntoCanonical(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
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
	if documents.document.OriginalFilename != "readme.txt" || documents.document.DOI != "10.1000/example-paper" || documents.document.SourceState != types.DocumentSourceAvailable {
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

func TestUploadRouteReturnsCreatedDocumentWhenInitialQueueFails(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	documents := &documentCreatorStub{}
	documentService := service.NewDocumentServiceWithProcessing(
		config.Config{MaxUploadSize: fileservice.DefaultMaxUploadSize},
		documents,
		storage,
		nil,
		&failingDocumentTaskEnqueuer{},
		nil,
	)
	documentHandler := handler.NewDocumentHandler(config.Config{MaxUploadSize: fileservice.DefaultMaxUploadSize}, documentService)
	processHandler := handler.NewDocumentProcessHandler(documentService)
	engine := router.NewRouter(router.RouterParams{DocumentHandler: documentHandler, DocumentProcessHandler: processHandler})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "retryable.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("durable despite queue failure")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/document/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if documents.document.ID == uuid.Nil || documents.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("persisted document = %#v, want failed markdown stage", documents.document)
	}
	if !strings.Contains(response.Body.String(), documents.document.ID.String()) ||
		!strings.Contains(response.Body.String(), `"processingQueued":false`) ||
		!strings.Contains(response.Body.String(), `"retryable":true`) {
		t.Fatalf("response must expose retryable created document: %s", response.Body.String())
	}
	if _, err := storage.Open(context.Background(), documents.document.StorageKey); err != nil {
		t.Fatalf("stored source must remain available for retry: %v", err)
	}
	retryRequest := httptest.NewRequest(http.MethodPost, "/api/document/"+documents.document.ID.String()+"/retry", nil)
	retryResponse := httptest.NewRecorder()
	engine.ServeHTTP(retryResponse, retryRequest)
	if retryResponse.Code != http.StatusAccepted ||
		!strings.Contains(retryResponse.Body.String(), `"processingQueued":false`) ||
		documents.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("retry queue failure response=%d body=%s document=%#v", retryResponse.Code, retryResponse.Body.String(), documents.document)
	}
}

func TestUploadRouteRejectsDuplicateFileHash(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	documents := &documentCreatorStub{}
	documentService := service.NewDocumentService(documents, storage)
	documentHandler := handler.NewDocumentHandler(config.Config{MaxUploadSize: fileservice.DefaultMaxUploadSize}, documentService)
	processHandler := handler.NewDocumentProcessHandler(documentService)
	engine := router.NewRouter(router.RouterParams{DocumentHandler: documentHandler, DocumentProcessHandler: processHandler})

	upload := func() *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "same.txt")
		if err != nil {
			t.Fatalf("create multipart file: %v", err)
		}
		if _, err := part.Write([]byte("identical upload")); err != nil {
			t.Fatalf("write multipart file: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close multipart body: %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/document/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		return response
	}

	if response := upload(); response.Code != http.StatusCreated {
		t.Fatalf("first upload status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := upload(); response.Code != http.StatusConflict {
		t.Fatalf("duplicate upload status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUploadBatchRouteReportsAcceptedDuplicateAndRejectedItems(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	documents := &documentCreatorStub{}
	documentService := service.NewDocumentService(documents, storage)
	documentHandler := handler.NewDocumentHandler(config.Config{MaxUploadSize: fileservice.DefaultMaxUploadSize}, documentService)
	engine := router.NewRouter(router.RouterParams{DocumentHandler: documentHandler})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, input := range []struct {
		filename string
		content  string
	}{{"paper.txt", "same document"}, {"copy.txt", "same document"}, {"empty.txt", ""}} {
		part, createErr := writer.CreateFormFile("files", input.filename)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := part.Write([]byte(input.content)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/document/batch-upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result types.DocumentBatchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("items = %#v", result.Items)
	}
	if result.Items[0].Status != types.DocumentImportItemAccepted || result.Items[0].Document == nil ||
		result.Items[0].ProcessingQueued == nil || !*result.Items[0].ProcessingQueued {
		t.Fatalf("first item = %#v", result.Items[0])
	}
	if result.Items[1].Status != types.DocumentImportItemDuplicate || result.Items[1].Document == nil ||
		result.Items[1].Document.ID != result.Items[0].Document.ID {
		t.Fatalf("second item = %#v", result.Items[1])
	}
	if result.Items[2].Status != types.DocumentImportItemRejected || result.Items[2].Error == "" {
		t.Fatalf("third item = %#v", result.Items[2])
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
